"""Коды ошибок контракта v1 и их HTTP-статусы."""

from __future__ import annotations

from typing import Any

HTTP_BY_CODE: dict[str, int] = {
    "INVALID_REQUEST": 400,
    "UNSUPPORTED_EVENT": 400,
    "CONTRACT_VERSION_UNSUPPORTED": 400,
    "INVALID_SERVICE_TOKEN": 401,
    "ACCESS_DENIED": 403,
    "ADMIN_REQUIRED": 403,
    "CANNOT_START_TRIP": 403,
    "NOT_FOUND": 404,
    "VEHICLE_UNAVAILABLE": 409,
    "USER_BUSY": 409,
    "STALE_VERSION": 409,
    "HOLD_EXPIRED": 409,
    "INVALID_STATE": 409,
    "IDEMPOTENCY_CONFLICT": 409,
    "COMMAND_IN_PROGRESS": 409,
    "LEASE_EXPIRED": 409,
    "RULES_REQUIRED": 422,
    "CHALLENGE_EXPIRED": 422,
    "FILE_TOO_LARGE": 413,
    "UNSUPPORTED_MEDIA": 415,
    "PHOTO_SET_INCOMPLETE": 422,
    "DUPLICATE_PHOTO": 422,
    "ODOMETER_ROLLBACK": 422,
    "LOCATION_REQUIRED": 422,
    "UNSAFE_RETURN": 422,
    "CHALLENGE_INVALID": 422,
    "RATE_LIMITED": 429,
    "DATABASE_UNAVAILABLE": 503,
    "STORAGE_UNAVAILABLE": 503,
    "TEMPORARY_FAILURE": 503,
}

MESSAGES: dict[str, str] = {
    "INVALID_REQUEST": "Некорректный запрос",
    "INVALID_SERVICE_TOKEN": "Сервис не авторизован",
    "ACCESS_DENIED": "Доступ запрещён",
    "ADMIN_REQUIRED": "Требуются права администратора",
    "CANNOT_START_TRIP": "Новые поездки запрещены",
    "NOT_FOUND": "Не найдено",
    "VEHICLE_UNAVAILABLE": "Автомобиль недоступен",
    "USER_BUSY": "Уже есть активное оформление или поездка",
    "STALE_VERSION": "Данные устарели, обновите экран",
    "HOLD_EXPIRED": "Время оформления истекло",
    "INVALID_STATE": "Операция недоступна на текущем шаге",
    "IDEMPOTENCY_CONFLICT": "Ключ идемпотентности уже использован для другого запроса",
    "COMMAND_IN_PROGRESS": "Команда уже выполняется",
    "LEASE_EXPIRED": "Аренда события истекла",
    "RULES_REQUIRED": "Нужно принять правила",
    "CHALLENGE_EXPIRED": "Срок примера истёк",
    "CHALLENGE_INVALID": "Неверное подтверждение",
    "FILE_TOO_LARGE": "Файл слишком большой",
    "UNSUPPORTED_MEDIA": "Неподдерживаемый формат изображения",
    "PHOTO_SET_INCOMPLETE": "Не хватает фотографий",
    "DUPLICATE_PHOTO": "Эта фотография уже загружена",
    "ODOMETER_ROLLBACK": "Пробег меньше предыдущего значения",
    "LOCATION_REQUIRED": "Укажите место парковки",
    "UNSAFE_RETURN": "Возврат невозможен без закрытой машины, ключей и допустимой парковки",
    "DATABASE_UNAVAILABLE": "База данных недоступна",
    "STORAGE_UNAVAILABLE": "Хранилище недоступно",
    "TEMPORARY_FAILURE": "Временная ошибка",
}


class DomainError(Exception):
    def __init__(self, code: str, *, current_version: int | None = None,
                 missing_slots: list[int] | None = None) -> None:
        super().__init__(code)
        if code not in HTTP_BY_CODE:
            raise ValueError(f"unknown error code {code}")
        self.code = code
        self.current_version = current_version
        self.missing_slots = missing_slots

    @property
    def http_status(self) -> int:
        return HTTP_BY_CODE[self.code]

    def body(self, request_id: str) -> dict[str, Any]:
        error: dict[str, Any] = {
            "code": self.code,
            "message": MESSAGES.get(self.code, self.code),
            "retryable": self.http_status in (429, 503),
        }
        details: dict[str, Any] = {}
        if self.current_version is not None:
            details["current_version"] = self.current_version
        if self.missing_slots is not None:
            details["missing_slots"] = self.missing_slots
        if details:
            error["details"] = details
        return {"error": error, "request_id": request_id}
