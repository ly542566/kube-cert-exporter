# ---- 构建阶段 ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# 先只拷依赖清单，利用层缓存：改代码不会触发重新下载依赖
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/exporter .

# ---- 运行阶段 ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/exporter /exporter
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/exporter"]
