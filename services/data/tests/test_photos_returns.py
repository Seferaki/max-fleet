"""Фото (AC-06…AC-08), возврат (AC-15…AC-19), замечания и сбои хранилища."""

from __future__ import annotations

from conftest import (
    ADMIN,
    DRIVER,
    DRIVER2,
    V1,
    Api,
    begin_return,
    fill_return,
    png,
    start_trip,
    take_until_inspection,
    upload_all,
)


def test_photo_slots_duplicates_and_replace(api: Api, store: object) -> None:
    checkout = take_until_inspection(api, DRIVER, V1)
    insp = checkout["inspection"]
    image = png()
    first = api.upload(DRIVER, insp["id"], 1, insp["version"], image, event="message:1:message_created",
                       key="photo-key-001")
    assert first.status_code == 200, first.text
    data = first.json()["data"]
    replay = api.upload(DRIVER, insp["id"], 1, insp["version"], image, event="message:1:message_created",
                        key="photo-key-001")  # AC-07: дубликат события
    assert replay.status_code == 200 and replay.json()["data"] == data
    dup_hash = api.upload(DRIVER, insp["id"], 2, data["inspection"]["version"], image)
    assert dup_hash.status_code == 422 and dup_hash.json()["error"]["code"] == "DUPLICATE_PHOTO"
    insp = data["inspection"]
    insp = upload_all(api, DRIVER, insp, range(2, 8))  # 7/8 — AC-06
    seven = api.cmd(DRIVER, "inspection.confirm_photos", insp["id"], insp["version"])
    assert seven.status_code == 422 and seven.json()["error"]["details"]["missing_slots"] == [8]
    insp = upload_all(api, DRIVER, insp, range(8, 9))
    confirmed = api.agg(api.cmd(DRIVER, "inspection.confirm_photos", insp["id"], insp["version"]))
    assert confirmed["photos_confirmed_at"] and confirmed["missing_slots"] == []
    replaced = api.ok(api.upload(DRIVER, insp["id"], 3, confirmed["version"], png()))  # AC-08
    assert replaced["inspection"]["occupied_slots"] == list(range(1, 9))
    assert replaced["inspection"]["photos_confirmed_at"] is None
    assert replaced["inspection"]["version"] == confirmed["version"] + 1
    bad_slot = api.upload(DRIVER, insp["id"], 9, replaced["inspection"]["version"], png())
    assert bad_slot.status_code == 400
    content = api.client.get(f"/internal/v1/assets/{data['asset_id']}/content", headers=api.headers(DRIVER))
    assert content.status_code == 200 and content.content == image
    assert content.headers["cache-control"] == "private, no-store"
    foreign = api.client.get(f"/internal/v1/assets/{data['asset_id']}/content", headers=api.headers(DRIVER2))
    assert foreign.status_code == 404
    admin = api.client.get(f"/internal/v1/assets/{data['asset_id']}/content", headers=api.headers(ADMIN))
    assert admin.status_code == 200


def test_photo_validation_and_storage_failure(api: Api, store: object) -> None:
    checkout = take_until_inspection(api, DRIVER, V1)
    insp = checkout["inspection"]
    fake = api.upload(DRIVER, insp["id"], 1, insp["version"], b"not an image")
    assert fake.status_code == 415 and fake.json()["error"]["code"] == "UNSUPPORTED_MEDIA"
    mismatch = api.upload(DRIVER, insp["id"], 1, insp["version"], png(), mime="image/jpeg")
    assert mismatch.status_code == 415
    video = api.upload(DRIVER, insp["id"], 1, insp["version"], b"\x00" * 100, mime="video/mp4")
    assert video.status_code == 415
    store.fail_put = True  # type: ignore[attr-defined]
    failed = api.upload(DRIVER, insp["id"], 1, insp["version"], png())
    assert failed.status_code == 503 and failed.json()["error"]["code"] == "STORAGE_UNAVAILABLE"
    store.fail_put = False  # type: ignore[attr-defined]
    after = api.ok(api.get(f"/inspections/{insp['id']}", DRIVER))
    assert after["occupied_slots"] == [] and after["version"] == insp["version"]
    stale = api.upload(DRIVER, insp["id"], 1, insp["version"] + 5, png())
    assert stale.status_code == 409 and stale.json()["error"]["code"] == "STALE_VERSION"


def test_issue_before_trip_blocks_vehicle(api: Api) -> None:
    """AC-09: замечание до выезда отменяет оформление и блокирует машину."""
    checkout = take_until_inspection(api, DRIVER, V1)
    insp = checkout["inspection"]
    staged = api.client.post("/internal/v1/assets/stage", headers=api.headers(DRIVER, key="stage-key-01"),
                             data={"purpose": "issue", "scope_type": "inspection", "scope_id": insp["id"],
                                   "source_event_key": "message:9:message_created"},
                             files={"image": ("p.png", png(), "image/png")})
    assert staged.status_code == 200, staged.text
    asset_id = staged.json()["data"]["asset_id"]
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))
    issue = api.agg(api.cmd(DRIVER, "issue.create", V1, vehicle["version"], {
        "category": "body_damage", "description": "Царапина на двери", "inspection_id": insp["id"],
        "asset_ids": [asset_id]}))
    assert issue["stage"] == "before" and issue["asset_ids"] == [asset_id] and issue["blocks_issuance"]
    assert api.ok(api.get(f"/checkouts/{checkout['id']}", DRIVER))["status"] == "rejected"
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))
    assert vehicle["status"] == "unavailable" and vehicle["needs_review"]
    listed = api.ok(api.get("/vehicles", DRIVER, available="true", limit="50"))
    assert V1 not in {v["id"] for v in listed["items"]}
    admin_issues = api.ok(api.get("/admin/issues", ADMIN, status="open"))
    assert [i["id"] for i in admin_issues["items"]] == [issue["id"]]
    claim = api.ok(api.worker("/notifications/claim", {"worker_id": "w", "max_items": 5}))
    assert [i["event"]["type"] for i in claim["items"]] == ["issue_created"]


def test_unsafe_return_and_cancel(api: Api) -> None:
    trip = start_trip(api, DRIVER, V1)
    ret = begin_return(api, DRIVER, trip)
    ret = fill_return(api, DRIVER, ret, keys_returned=False)
    unsafe = api.cmd(DRIVER, "return.complete", ret["id"], ret["version"], {"attestation": True})
    assert unsafe.status_code == 422 and unsafe.json()["error"]["code"] == "UNSAFE_RETURN"  # AC-18
    assert api.ok(api.get(f"/trips/{trip['id']}", DRIVER))["status"] == "returning"
    cancelled = api.agg(api.cmd(DRIVER, "return.cancel", ret["id"], ret["version"]))
    assert cancelled["status"] == "cancelled"
    trip = api.ok(api.get(f"/trips/{trip['id']}", DRIVER))
    assert trip["status"] == "active" and trip["return_id"] is None
    assert api.ok(api.get(f"/vehicles/{V1}", DRIVER2))["status"] == "in_trip"  # AC-19
    ret2 = begin_return(api, DRIVER, trip)
    assert ret2["id"] != ret["id"] and ret2["parking_location"] is None
    assert ret2["inspection"]["occupied_slots"] == []


def test_foreign_location_rejected(api: Api) -> None:
    """AC-15: чужой возврат не принимает точку."""
    trip = start_trip(api, DRIVER, V1)
    ret = begin_return(api, DRIVER, trip)
    forged = api.cmd(DRIVER2, "return.set_location", ret["id"], ret["version"], {
        "latitude": 55.0, "longitude": 37.0, "source": "manual_map", "confirmed": True})
    assert forged.status_code == 404
    no_confirm = api.cmd(DRIVER, "return.set_location", ret["id"], ret["version"], {
        "latitude": 55.0, "longitude": 37.0, "source": "manual_map", "confirmed": False})
    assert no_confirm.status_code == 400
    location_missing = fill_return(api, DRIVER, ret)
    assert location_missing["parking_location"]["landmark"] == "У входа"


def test_return_with_damage_blocks_next_issue(api: Api) -> None:
    """AC-17: возврат с повреждением завершается, машина недоступна до проверки."""
    trip = start_trip(api, DRIVER, V1)
    ret = begin_return(api, DRIVER, trip)
    ret = fill_return(api, DRIVER, ret, new_damage=True)
    no_issue = api.cmd(DRIVER, "return.complete", ret["id"], ret["version"], {"attestation": True})
    assert no_issue.status_code == 409  # сначала нужно описать повреждение
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))
    api.agg(api.cmd(DRIVER, "issue.create", V1, vehicle["version"], {
        "category": "body_damage", "description": "Вмятина", "inspection_id": ret["inspection"]["id"],
        "asset_ids": []}))
    ret = api.ok(api.get(f"/returns/{ret['id']}", DRIVER))
    done = api.agg(api.cmd(DRIVER, "return.complete", ret["id"], ret["version"], {"attestation": True}))
    assert done["status"] == "completed"
    vehicle = api.ok(api.get(f"/vehicles/{V1}", DRIVER))
    assert vehicle["status"] == "unavailable" and vehicle["needs_review"]
    trip = api.ok(api.get(f"/trips/{trip['id']}", ADMIN))
    assert trip["issues"][0]["stage"] == "after"


def test_double_complete_race(api: Api) -> None:
    from concurrent.futures import ThreadPoolExecutor

    trip = start_trip(api, DRIVER, V1)
    ret = fill_return(api, DRIVER, begin_return(api, DRIVER, trip))
    with ThreadPoolExecutor(4) as pool:
        codes = list(pool.map(lambda _: api.cmd(DRIVER, "return.complete", ret["id"], ret["version"],
                                                {"attestation": True}).status_code, range(4)))
    assert codes.count(200) == 1, codes
