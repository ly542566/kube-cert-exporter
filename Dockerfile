# ---- 构建阶段 ----
FROM golang:1.27-alpine AS build
WORKDIR /src

# 依赖已 vendor 到仓库，构建过程完全不需要访问网络
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/exporter .

# ---- 运行阶段 ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/exporter /exporter
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/exporter"]
