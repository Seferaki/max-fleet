#!/bin/sh
# SeaweedFS S3: конфиг доступа собирается из docker secrets во временный файл (не в образ и не в лог).
set -eu
umask 077
access_key=$(cat /run/secrets/s3_access_key)
secret_key=$(cat /run/secrets/s3_secret_key)
printf '{"identities":[{"name":"data-api","credentials":[{"accessKey":"%s","secretKey":"%s"}],"actions":["Admin","Read","Write","List","Tagging"]}]}' \
  "$access_key" "$secret_key" > /tmp/s3.json
exec weed server -dir=/data -ip.bind=0.0.0.0 -s3 -s3.port=8333 -s3.config=/tmp/s3.json \
  -master.volumeSizeLimitMB=1024 -volume.max=0
