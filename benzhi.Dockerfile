# 评测用镜像：交付 Dockerfile 固定在仓库根目录，保留完整 Go 工具链。
FROM golang:1.22

# Go + 前端工程：额外安装 Node.js 20，保留两套工具链
RUN apt-get update && apt-get install -y curl \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY backend/go.mod backend/go.sum backend/
WORKDIR /app/backend
RUN go mod download
WORKDIR /app
COPY frontend/package*.json frontend/
RUN cd /app/frontend && npm install
COPY . .
WORKDIR /app/backend
RUN go build ./...
RUN go build -o /app/.runtime-bin ./cmd/server
CMD ["/app/.runtime-bin"]
RUN cd /app/frontend && npm run build

# 多架构交叉构建示例（请在仓库根目录执行）：
# docker buildx build --platform linux/arm64,linux/amd64 -f benzhi.Dockerfile -t <image> .
