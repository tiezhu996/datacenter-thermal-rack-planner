#!/bin/bash
# 请在仓库根目录运行；第二个参数为目标平台（arm64 / amd64）。
set -euo pipefail
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
cd "$SCRIPT_DIR"
IMAGE_NAME=${1:-my-go-task}
PLATFORM=${2:-linux/amd64}

docker buildx build --platform "$PLATFORM" -f benzhi.Dockerfile -t "$IMAGE_NAME" .

echo ""
echo "✅ Docker image '$IMAGE_NAME' built successfully!"
echo "▶️  运行镜像: docker run --rm $IMAGE_NAME"
echo "📋 调试容器: docker run -it --entrypoint bash $IMAGE_NAME"
