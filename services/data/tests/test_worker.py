"""data-worker: orphan cleanup не трогает привязанные фото; hold sweep освобождает машины."""

from __future__ import annotations

from sqlalchemy import text

from app.worker import run_once
from conftest import DRIVER, V1, Api, png, take_until_inspection


def test_orphan_cleanup_keeps_referenced(api: Api, app_and_store: tuple) -> None:
    app, store = app_and_store
    checkout = take_until_inspection(api, DRIVER, V1)
    insp = checkout["inspection"]
    attached = api.ok(api.upload(DRIVER, insp["id"], 1, insp["version"], png()))
    staged = api.client.post("/internal/v1/assets/stage", headers=api.headers(DRIVER, key="stage-orphan-1"),
                             data={"purpose": "issue", "scope_type": "vehicle", "scope_id": V1,
                                   "source_event_key": "message:o1:message_created"},
                             files={"image": ("p.png", png(), "image/png")})
    assert staged.status_code == 200, staged.text
    orphan_id = staged.json()["data"]["asset_id"]
    assert len(store.objects) == 2
    with app.state.engine.begin() as conn:
        conn.execute(text("UPDATE photo_assets SET staging_expires_at = now() - interval '1 minute' "
                          "WHERE state = 'staged'"))
        conn.execute(text("UPDATE photo_assets SET staging_expires_at = now() - interval '1 minute', "
                          "state = 'staged' WHERE id = :id"), {"id": attached["asset_id"]})
    stats = run_once(app.state.factory, store)
    assert stats["orphans_removed"] == 1
    with app.state.engine.connect() as conn:
        states = dict(conn.execute(text("SELECT id::text, state FROM photo_assets")).all())
    assert states[orphan_id] == "deleted"
    assert states[attached["asset_id"]] == "staged"  # привязан к ракурсу — не удалён
    assert len(store.objects) == 1


def test_sweep_releases_expired_hold(api: Api, app_and_store: tuple) -> None:
    app, store = app_and_store
    take_until_inspection(api, DRIVER, V1)
    with app.state.engine.begin() as conn:
        conn.execute(text("UPDATE checkout_attempts SET expires_at = now() - interval '1 second'"))
    assert run_once(app.state.factory, store)["holds_expired"] == 1
    with app.state.engine.connect() as conn:
        assert conn.execute(text("SELECT count(*) FROM vehicle_assignments")).scalar_one() == 0
