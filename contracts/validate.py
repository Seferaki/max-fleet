"""Validate the checked-in contracts, representative fixtures and scenario catalog."""

import json
import copy
import subprocess
import sys
from pathlib import Path

import jsonschema
import yaml
from openapi_spec_validator import validate_spec


ROOT = Path(__file__).resolve().parent


def load_json(relative):
    return json.loads(ROOT.joinpath(relative).read_text(encoding="utf-8"))


def schema_for(document, name):
    """Turn OpenAPI component fragments into a self-contained JSON Schema."""

    def convert(value):
        if isinstance(value, list):
            return [convert(item) for item in value]
        if isinstance(value, dict):
            return {key: (item.replace("#/components/schemas/", "#/$defs/")
                          if key == "$ref" and isinstance(item, str) else convert(item))
                    for key, item in value.items()}
        return value

    result = convert(document["components"]["schemas"][name])
    result["$defs"] = {key: convert(value) for key, value
                       in document["components"]["schemas"].items()}
    jsonschema.Draft202012Validator.check_schema(result)
    return result


def check_example(document, relative, schema_name):
    payload = load_json(relative)
    jsonschema.Draft202012Validator(schema_for(document, schema_name),
                                     format_checker=jsonschema.FormatChecker()).validate(payload)
    return payload


def main():
    before = ROOT.joinpath("data-api.openapi.yaml").read_bytes()
    subprocess.run([sys.executable, str(ROOT / "build_openapi.py")], check=True)
    after = ROOT.joinpath("data-api.openapi.yaml").read_bytes()
    if before != after:
        raise RuntimeError("data-api.openapi.yaml не совпадает с build_openapi.py")

    data = yaml.safe_load(after)
    map_api = yaml.safe_load(ROOT.joinpath("map-api.openapi.yaml").read_text(encoding="utf-8"))
    validate_spec(data)
    validate_spec(map_api)

    commands = load_json("examples/commands.json")
    command_schema = schema_for(data, "Command")
    command_validator = jsonschema.Draft202012Validator(command_schema,
                                                        format_checker=jsonschema.FormatChecker())
    for number, command in enumerate(commands, 1):
        errors = list(command_validator.iter_errors(command))
        if errors:
            raise RuntimeError(f"commands.json #{number}: {errors[0].message}")
    operations = [item["operation"] for item in commands]
    if len(set(operations)) != len(operations):
        raise RuntimeError("Повтор operation в commands.json")
    declared = set(data["components"]["schemas"]["Command"]["discriminator"]["mapping"])
    if set(operations) != declared:
        raise RuntimeError("Примеры не покрывают все command operations")
    invalid = copy.deepcopy(commands[5])
    invalid["payload"]["fuel_level"] = 37
    if command_validator.is_valid(invalid):
        raise RuntimeError("Неверный enum топлива был принят")
    invalid = copy.deepcopy(commands[0])
    invalid["payload"]["sql"] = "unexpected"
    if command_validator.is_valid(invalid):
        raise RuntimeError("Неизвестное поле команды было принято")
    invalid = copy.deepcopy(commands[8])
    del invalid["expected_version"]
    if command_validator.is_valid(invalid):
        raise RuntimeError("Команда без expected_version была принята")

    for filename, schema_name in (
        ("state-empty.json", "CurrentStateResponse"),
        ("issue-draft-state.json", "CurrentStateResponse"),
        ("hold-expired.json", "ErrorResponse"),
        ("previous-inspection.json", "InspectionResponse"),
        ("inbox-photo.json", "NormalizedEvent"),
    ):
        check_example(data, "examples/" + filename, schema_name)
    for filename, schema_name in (
        ("map-context.json", "ContextResponse"),
        ("map-location-request.json", "LocationRequest"),
        ("map-location-response.json", "LocationResponse"),
    ):
        check_example(map_api, "examples/" + filename, schema_name)
    bad_map_request = load_json("examples/map-location-request.json")
    bad_map_request["confirmed"] = False
    if jsonschema.Draft202012Validator(schema_for(map_api, "LocationRequest")).is_valid(bad_map_request):
        raise RuntimeError("Карта приняла точку без явного подтверждения")

    seed = load_json("examples/synthetic-seed.json")
    if seed.get("demo_only") is not True or len(seed["vehicles"]) != 10:
        raise RuntimeError("Нужны ровно 10 демонстрационных машин")
    if len({vehicle["id"] for vehicle in seed["vehicles"]}) != 10:
        raise RuntimeError("Повтор ID машины")
    if len({vehicle["plate"] for vehicle in seed["vehicles"]}) != 10:
        raise RuntimeError("Повтор демонстрационного номера")
    if any(vehicle["fuel_level"] not in (0, 25, 50, 75, 100)
           or vehicle["odometer_km"] < 0 for vehicle in seed["vehicles"]):
        raise RuntimeError("Seed расходится с уровнями топлива/одометром контракта")
    if len(seed["employees"]) < 3:
        raise RuntimeError("Нет тестовых ролей")

    scenarios = load_json("scenarios/v1.json")
    if scenarios["contract_version"] != data["info"]["version"]:
        raise RuntimeError("Версия сценариев не совпадает с OpenAPI")
    photo_read = load_json("examples/trip-photo-read.json")
    post_return = load_json("examples/post-return-issue.json")
    if post_return["contract_version"] != data["info"]["version"]:
        raise RuntimeError("Версия post-return примера не совпадает с контрактом")
    jsonschema.Draft202012Validator(schema_for(data, "IssueCreateCommand"),
                                     format_checker=jsonschema.FormatChecker()).validate(post_return["request"])
    if (post_return["request"]["payload"]["trip_id"] != post_return["completed_trip_id"]
            or post_return["request"]["payload"]["inspection_id"] is not None
            or not all(post_return["expected"].values())
            or post_return["expected"]["issue_stage"] != "post_return"):
        raise RuntimeError("Неверная семантика post-return примера")
    post_return_conversation = check_example(
        data, "examples/post-return-conversation.json", "Conversation")
    post_context = post_return_conversation["context"]
    if (post_return_conversation["flow"] != "issue_post_return"
            or post_context["target_id"] != post_context["trip_id"]
            or post_context["issue_category"] not in {"parking", "car_lock"}
            or post_context["vehicle_id"] is None
            or post_context["vehicle_version"] is None
            or len(post_context["asset_ids"]) > 3):
        raise RuntimeError("Неверная семантика post-return conversation")
    issue_categories = load_json("examples/issue-categories.json")
    if (issue_categories["contract_version"] != data["info"]["version"]
            or {request["payload"]["category"] for request in issue_categories["requests"]}
            != {"parking", "car_lock"}):
        raise RuntimeError("Нет актуальных примеров новых категорий замечаний")
    issue_schema = schema_for(data, "IssueCreateCommand")
    for request in issue_categories["requests"]:
        jsonschema.Draft202012Validator(issue_schema,
                                         format_checker=jsonschema.FormatChecker()).validate(request)
        payload = request["payload"]
        if payload["trip_id"] is None or payload["inspection_id"] is not None:
            raise RuntimeError("Новые категории должны быть привязаны к поездке")
    invalid_flow = copy.deepcopy(post_return_conversation)
    invalid_flow["flow"] = "unknown_flow"
    if jsonschema.Draft202012Validator(schema_for(data, "Conversation")).is_valid(invalid_flow):
        raise RuntimeError("Неизвестный conversation flow был принят")
    route = "/internal/v1/trips/{id}/inspection-photos/{phase}/{slot}"
    if route not in data["paths"] or photo_read["contract_version"] != data["info"]["version"]:
        raise RuntimeError("Пример чтения фото не совпадает с контрактом")
    if photo_read["request_path"] != route.replace("{id}", photo_read["trip_id"]).replace("{phase}", photo_read["phase"]).replace("{slot}", str(photo_read["slot"])):
        raise RuntimeError("Неверный пример пути чтения фото")
    if photo_read["response"]["content_type"] not in data["paths"][route]["get"]["responses"]["200"]["content"]:
        raise RuntimeError("Неверный пример media type фото")
    areas = {"identity", "vehicles", "checkout", "inspection", "return", "delivery", "schema"}
    cases = scenarios["cases"]
    if len(cases) < 35 or {case["area"] for case in cases} != areas:
        raise RuntimeError("Неполный сценарный набор")
    if len({case["id"] for case in cases}) != len(cases):
        raise RuntimeError("Повтор ID сценария")
    error_codes = set(data["components"]["schemas"]["ErrorCode"]["enum"])
    error_codes |= set(map_api["components"]["schemas"]["ErrorResponse"]["properties"]["error"]["properties"]["code"]["enum"])
    for case in cases:
        expected = case["expect"]
        if not case["id"] or not case["given"] or not case["when"] or not expected["facts"]:
            raise RuntimeError(f"Пустой сценарий: {case['id']}")
        if expected["error"] is not None and expected["error"] not in error_codes:
            raise RuntimeError(f"Неизвестный error code: {case['id']}")

    other_example_count = len(list((ROOT / "examples").glob("*.json"))) - 1
    print(f"OK: 2 OpenAPI, {len(data['paths'])} data routes, "
          f"{len(commands)} command examples, {other_example_count} other examples, {len(cases)} scenarios")


if __name__ == "__main__":
    main()
