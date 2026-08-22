# datacenter-thermal-rack-planner__010

## 构建镜像

请从**仓库根目录**执行；`benzhi.Dockerfile`、`build_benzhi_docker.sh`、`BENZHI_README.md` 均固定在该目录：

```bash
./build_benzhi_docker.sh <image-name> [linux/amd64|linux/arm64]
```

## 标准命令

```bash
go build ./...     # 编译
go run ./cmd/server   # 启动
go test ./...      # 测试（如有）
```

```bash
cd frontend && npm install   # 前端依赖（镜像构建阶段已预装）
cd frontend && npm run build   # 构建前端
```

## 环境

- 基础镜像: golang:1.22
- Go 模块目录: `backend`
- 依赖已在镜像构建阶段预下载，容器内离线可用。
- 容器内工作目录: `/app/backend`
- 前端目录: `frontend`（Node.js 20，npm 依赖已在镜像构建阶段预下载）
