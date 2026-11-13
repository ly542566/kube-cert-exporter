# 多阶段构建：任何人 clone 下来执行 docker build 就能出镜像，不依赖 CI 预先编译。
# 依赖走 vendor（仓库里自带），构建过程不需要访问外网。
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/kube-cert-exporter .

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/kube-cert-exporter /kube-cert-exporter
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/kube-cert-exporter"]
