"""Адаптер приватного S3-совместимого хранилища фотографий."""

from __future__ import annotations

from typing import Any, Protocol

import boto3
from botocore.config import Config as BotoConfig
from botocore.exceptions import BotoCoreError, ClientError

from app.config import Settings


class StorageUnavailable(RuntimeError):
    pass


class ObjectStore(Protocol):
    def put(self, key: str, data: bytes, content_type: str) -> None: ...
    def get(self, key: str) -> bytes: ...
    def delete(self, key: str) -> None: ...
    def healthy(self) -> bool: ...


class S3ObjectStore:
    def __init__(self, settings: Settings, client: Any = None) -> None:
        self.bucket = settings.s3_bucket
        self.client: Any = client or boto3.client(
            "s3",
            endpoint_url=settings.s3_endpoint,
            region_name=settings.s3_region,
            aws_access_key_id=settings.s3_access_key,
            aws_secret_access_key=settings.s3_secret_key,
            config=BotoConfig(connect_timeout=2, read_timeout=10,
                              retries={"max_attempts": 2, "mode": "standard"},
                              s3={"addressing_style": "path"}),
        )

    def ensure_bucket(self) -> None:
        try:
            self.client.head_bucket(Bucket=self.bucket)
        except ClientError:
            try:
                self.client.create_bucket(Bucket=self.bucket)
            except (BotoCoreError, ClientError) as exc:
                raise StorageUnavailable("bucket") from exc
        except BotoCoreError as exc:
            raise StorageUnavailable("bucket") from exc

    def put(self, key: str, data: bytes, content_type: str) -> None:
        try:
            self.client.put_object(Bucket=self.bucket, Key=key, Body=data,
                                   ContentType=content_type)
        except (BotoCoreError, ClientError) as exc:
            raise StorageUnavailable("put") from exc

    def get(self, key: str) -> bytes:
        try:
            response = self.client.get_object(Bucket=self.bucket, Key=key)
            return response["Body"].read()
        except (BotoCoreError, ClientError) as exc:
            raise StorageUnavailable("get") from exc

    def delete(self, key: str) -> None:
        try:
            self.client.delete_object(Bucket=self.bucket, Key=key)
        except (BotoCoreError, ClientError) as exc:
            raise StorageUnavailable("delete") from exc

    def healthy(self) -> bool:
        try:
            self.client.head_bucket(Bucket=self.bucket)
            return True
        except (BotoCoreError, ClientError):
            return False


class MemoryObjectStore:
    """Хранилище для unit-тестов и проверки отказов."""

    def __init__(self) -> None:
        self.objects: dict[str, tuple[bytes, str]] = {}
        self.fail_put = False
        self.fail_get = False

    def put(self, key: str, data: bytes, content_type: str) -> None:
        if self.fail_put:
            raise StorageUnavailable("put")
        self.objects[key] = (data, content_type)

    def get(self, key: str) -> bytes:
        if self.fail_get or key not in self.objects:
            raise StorageUnavailable("get")
        return self.objects[key][0]

    def delete(self, key: str) -> None:
        self.objects.pop(key, None)

    def healthy(self) -> bool:
        return not self.fail_put
