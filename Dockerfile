# 运行时镜像。
# 二进制不在这里编译 —— CI 的 go 容器已提前编好（Jenkinsfile Test 阶段产出 bin/exporter），
# kaniko 只做打包。1 核节点上省掉这轮重复编译约 4 分钟。
# 本地构建前先执行：go build -o bin/exporter .
FROM gcr.io/distroless/static-debian12
COPY bin/exporter /kube-cert-exporter
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/kube-cert-exporter"]
