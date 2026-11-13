# CI/CD 链路复现手册

从 4 台裸机到「改一行代码，4 分钟内线上生效」的完整步骤。

> **安全提醒**：本文件会进 Git。所有密码、token 一律用占位符，
> 不要把真实值填进来。真实值只存在各机器的本地文件里。

---

## 0. 这份文档怎么用

- 复选框按顺序走，**前一段没通过验收不要往后走**，后面的坑都会变成连锁故障。
- 每段的「坑」都是实际踩过的，按顺序走可以避开。
- 环境相关的参数统一用 `<占位符>`，按自己的网络填。
- 总耗时约 8-10 小时，建议拆两个周末。

---

## 1. 目标拓扑

| 角色 | 主机名 | IP | 规格 | 负载 |
|---|---|---|---|---|
| 控制面 | master | `<MASTER_IP>` | 2C4G | apiserver / etcd |
| 工作节点 | node1 | `<NODE1_IP>` | 2C4G | Harbor + NFS server |
| 工作节点 | node2 | `<NODE2_IP>` | 2C4G | Jenkins controller + ArgoCD |
| 工作节点 | node3 | `<NODE3_IP>` | 2C4G | 构建 Pod + 监控栈 |

内存账（2C4G 的机器，实测）：

```
系统 + kubelet + containerd     ≈ 600 MiB
Harbor 全套                     ≈ 800 MiB（node1）
Jenkins controller              ≈ 1.5 GiB（node2）
ArgoCD 精简版                   ≈ 600 MiB（node2）
构建 Pod（go+kaniko 峰值）      ≈ 1.1 GiB（node3）
监控栈（prom+grafana+alertmgr） ≈ 1.5 GiB（node3）
```

**这就是为什么 3 台不够、必须加 node3 的原因**——不是配置问题，是物理内存问题。
`2C4G × 3` 跑完这套，任何一次构建峰值都会把某个节点打挂。

端口分配：

| 服务 | 端口 | 类型 |
|---|---|---|
| Harbor | 30002 | NodePort (HTTP) |
| Jenkins | 30080 | NodePort |
| ArgoCD | 30081 | NodePort (HTTP，不走 TLS) |

组件版本：k8s 1.35.9 / containerd + pause 3.10.1 / Jenkins chart 5.12.0 /
ArgoCD chart 10.18.0 / kaniko v1.23.2-debug / golang 1.27-alpine。

---

## 2. 阶段 0：机器准备（约 1h）

- [ ] 4 台机器同一网段，互 ping 通
- [ ] 主机名设好（`hostnamectl set-hostname nodeX`），改完必须重新登录才生效
- [ ] 时间同步（`chronyd` 或 `systemd-timesyncd`），偏差 <1s
- [ ] 每台 root 能免密互访（或至少 master → 所有节点）

验收：`for i in ...; do ssh root@$i hostname; done` 全部返回正确主机名。

> 坑：新机器开机跑 `unattended-upgrades` 会占 dpkg 锁，装包报 `Could not get lock`。
> `systemctl stop unattended-upgrades`，**不要 rm 锁文件**。

---

## 3. 阶段 1：K8s 底座（约 2h）

每台机器：

- [ ] `swapoff -a`，注释 `/etc/fstab` 里的 swap 行
- [ ] 加载内核模块：`overlay`、`br_netfilter`
- [ ] sysctl：`net.bridge.bridge-nf-call-iptables=1`
- [ ] containerd 配置 `SystemdCgroup = true`
- [ ] containerd sandbox 镜像改成 `registry.k8s.io/pause:3.10.1`
- [ ] 安装 kubelet / kubeadm / kubectl 并 `apt-mark hold`（否则被自动升级会导致版本漂移）

初始化与加入：

- [ ] `kubeadm init --pod-network-cidr=...`（注意 CIDR 不要和内网网段冲突）
- [ ] 装 CNI
- [ ] `kubeadm join`（token 24h 过期，过期用 `kubeadm token create --print-join-command` 重新生成）

存储：

- [ ] node1 装 `nfs-server`，建导出目录
- [ ] **`/etc/exports` 必须逐个列出节点 IP**，不要用网段通配
- [ ] 所有节点装 `nfs-common`（**新加节点最容易漏这个**）
- [ ] 装 `nfs-client-provisioner`，建 StorageClass

验收：

```bash
kubectl get node                      # 全部 Ready
kubectl get sc                        # 有 nfs-client 且不是 default 之外的空
# 建个测试 PVC，确认能 Bound 且能写入
kubectl get pvc -A | grep -v Bound    # 应该没有输出
```

> 坑 1：新节点 `mount -t nfs` 报 `exit status 32` = 没装 nfs-common。
> 坑 2：PVC Pending，事件里 `access denied by server` = 服务端 exports 白名单没这个 IP，
> 改完必须 `exportfs -ra`。
> 坑 3：exports 改了不重载，等于没改。

---

## 4. 阶段 2：Harbor（约 1h）

- [ ] `helm repo add harbor https://helm.goharbor.io`
- [ ] 安装，关键配置项：
  - `expose.type=nodePort`，HTTP 端口 30002
  - `externalURL=http://<NODE1_IP>:30002`
  - 持久化指向已有 StorageClass
- [ ] UI 建项目 `library`（不要用默认的 library，默认是别人的）
- [ ] 建 robot 账号，权限：`repository: pull, push` + `artifact: read`
  - 级别选 **project** 而不是 system，最小权限
- [ ] 记录 `robot$<名字>` 和生成的 token（只显示一次）

**每台节点**都要配 insecure registry：

```bash
mkdir -p /etc/containerd/certs.d/<NODE1_IP>:30002
cat > /etc/containerd/certs.d/<NODE1_IP>:30002/hosts.toml <<'TOML'
server = "http://<NODE1_IP>:30002"
[host."http://<NODE1_IP>:30002"]
  capabilities = ["pull", "resolve", "push"]
  skip_verify = true
TOML
# 确认 config.toml 里有 config_path = '/etc/containerd/certs.d'
systemctl restart containerd
```

验收：**任意一台 node** 执行 `crictl pull <NODE1_IP>:30002/library/<某个镜像>` 成功。

> 坑：只配了 node1 自己，新节点拉镜像 `ImagePullBackOff`，事件里是 x509 或 http 拒绝。
> 这是 node3 加进来时踩的。

---

## 5. 阶段 3：Jenkins（约 2h，最容易卡住的一段）

### 5.1 装

- [ ] `helm repo add jenkins https://charts.jenkins.io`
- [ ] 准备 values，关键项：

```yaml
controller:
  admin: {username: admin, password: "<占位符>"}
  installPlugins: []        # 关键：见 5.2
  additionalPlugins: []
  nodeSelector: {kubernetes.io/hostname: node2}
  resources:
    requests: {cpu: 200m, memory: 512Mi}
    limits: {cpu: 1500m, memory: 1536Mi}
  javaOpts: "-Xms256m -Xmx768m"
  serviceType: NodePort
  nodePort: 30080
persistence:
  enabled: true
  storageClass: nfs-client
  size: 3Gi
agent:
  enabled: false            # 不用静态 agent，全走 K8s 动态 Pod
```

### 5.2 插件必须离线预装

init 容器的 `install-plugins.sh` 在国内网络下会**永久挂住**——`jenkins-plugin-cli`
没有读超时，连接僵死就一直等，日志停在某个插件数不动。

做法：起一个临时 Pod 挂同一个 PVC，把 hpi 直接丢进 `/var/jenkins_home/plugins`。

- [ ] 从 chart 默认 values 里抄出插件列表
- [ ] 逐个下载：`https://mirrors.aliyun.com/jenkins/plugins/<名>/<版本>/<名>.hpi`
- [ ] 或 `https://updates.jenkins.io/download/plugins/<名>/<版本>/<名>.hpi`（会 302 到 CDN，实测更快）
- [ ] 下载完 `ls -l` 检查大小，**半截文件会导致启动报 `2 failed plugins`**
- [ ] 清掉 `.bak`、`.tmp` 残留文件

> 坑：阿里云镜像偶尔给半截文件，而 Jenkins 只在启动日志里报数量，不报哪个坏了。
> 对照列表逐个数，缺的用官方源补。

### 5.3 JCasC 配 Kubernetes cloud

这是整段最容易翻车的地方。

```yaml
  JCasC:
    defaultConfig: false      # 关键！
    configScripts:
      kubernetes-cloud: |
        jenkins:
          clouds:
            - kubernetes:
                name: "kubernetes"
                serverUrl: "https://kubernetes.default.svc"   # 必须有 .svc
                namespace: "jenkins"
                jenkinsUrl: "http://jenkins.jenkins.svc.cluster.local:8080"
                webSocket: true
                skipTlsVerify: true
                templates:
                  - name: "go-builder"
                    label: "go"
                    nodeUsageMode: "EXCLUSIVE"
                    nodeSelector: "kubernetes.io/hostname=node3"
                    containers:
                      - name: go
                        image: golang:1.27-alpine
                        command: cat
                        ttyEnabled: true
                      - name: kaniko
                        image: gcr.io/kaniko-project/executor:v1.23.2-debug
                        command: cat
                        ttyEnabled: true
```

**三个必须理解的机制**：

1. chart 5.12.0 把 `configScripts` 的**每个 key 渲染成独立 ConfigMap**
   （`<release>-config-<key>`），不是塞进 `jcasc-config`。
   找不到配置时先 `helm get manifest | grep ConfigMap` 看清真实名字。
2. `defaultConfig: true` 时 chart 会**自己生成一个同名 cloud**
   （`serverUrl=https://kubernetes.default`，少 `.svc`，`webSocket=false`）。
   JCasC 的 clouds 是列表**追加**，Jenkins 只取第一个 → 自定义模板永远不会生效。
   必须关掉 `defaultConfig`，然后自己补 `securityRealm` / `numExecutors` / `location`。
3. `config-reload` sidecar **只监听它启动时记住的文件列表**，
   事后新增的 ConfigMap 文件它不看。改完只能重启 Pod。

验收（别只看 Pod 是 2/2）：

```bash
# 1. 渲染层
helm template jenkins jenkins/jenkins -n jenkins --version 5.12.0 -f values.yaml | grep -c "go-builder"

# 2. ConfigMap 层（注意真实名字）
kubectl -n jenkins get cm | grep config

# 3. 容器内挂载层
kubectl -n jenkins exec jenkins-0 -c jenkins -- ls /var/jenkins_home/casc_configs/

# 4. 运行时层（最重要）
kubectl -n jenkins exec jenkins-0 -c jenkins -- sh -c \
  'sed -n "/<clouds>/,/<\/clouds>/p" /var/jenkins_home/config.xml | grep -E "<name>|serverUrl"'
# 必须只有一个 cloud，且 serverUrl 带 .svc
```

> 坑：Jenkins 是 StatefulSet，`kubectl rollout restart` 对 STS 有效但有时不重建 Pod。
> 直接 `kubectl delete pod jenkins-0` 最干脆，PVC 数据不会丢。

---

## 6. 阶段 4：流水线（约 2h）

`Jenkinsfile` 三个 stage：Test → Build & Push → Update Git。

关键设计（每个都要能解释）：

| 设计 | 为什么 |
|---|---|
| kaniko 而不用 DinD | DinD 要 `privileged: true`，安全上过不去；且缓存难做 |
| Dockerfile 只 `COPY bin/exporter` | 否则 go 容器编译一遍、kaniko 里又 `go build` 一遍 |
| `GOCACHE=/gocache` + hostPath | 每次新 Pod 零缓存，`go vet` 要重编 vendor 几百个包 |
| `nodeUsageMode: EXCLUSIVE` | 独占节点，避免和其他 Pod 抢 CPU |
| tag 取 `GIT_COMMIT.take(7)` | 镜像和代码一一对应，可追溯；比 build number 有意义 |

- [ ] 建 Pipeline job：`Pipeline script from SCM`，分支 `*/main`，Script Path `Jenkinsfile`
- [ ] 建 Harbor 凭据（id `harbor-cred`，用户名是 `robot$xxx`，注意 `$` 要转义）
- [ ] 跑第一次，允许它慢（冷缓存 500s+ 正常）

验收：Harbor UI 出现 tag = commit 前 7 位的镜像，大小 ~11MB。

```bash
curl -u admin:<pass> "http://<NODE1_IP>:30002/api/v2.0/projects/library/repositories/kube-cert-exporter/artifacts?page_size=5"
```

> 坑：构建成功后 Pod 不回收，残留的 `go-builder-xxx` 会持续占 CPU。
> 模板里配 `podRetention: never`，或手动 `kubectl delete pod --force --grace-period=0`。

---

## 7. 阶段 5：回写与防死循环（约 1h）

这一步是 GitOps 闭环的接缝，也是最容易做成死循环的地方。

- [ ] GitHub 建 PAT（fine-grained 只给单仓库 `Contents: Read and write` 就够，别给全 repo 权限）
- [ ] 存成 Jenkins 凭据 `github-pat`
- [ ] Jenkinsfile 加 stage：

```groovy
stage('Update Git') {
  steps {
    // 注意：不要套 container('go')，golang:alpine 里没有 git
    withCredentials([usernamePassword(credentialsId: 'github-pat',
        usernameVariable: 'GH_USER', passwordVariable: 'GH_TOKEN')]) {
      sh '''
        printf 'https://%s:%s@github.com\n' "$GH_USER" "$GH_TOKEN" > /tmp/.gitcred
        trap 'rm -f /tmp/.gitcred' EXIT
        sed -i "s|^\( *image: \).*kube-cert-exporter:.*|\\1$REGISTRY/$IMAGE:$TAG|" deploy/20-deployment.yaml
        git add deploy/20-deployment.yaml
        git -c user.name=jenkins-ci \
            -c user.email=jenkins-ci@users.noreply.github.com \
            commit -m "ci: bump to $TAG [skip ci]"
        git -c credential.helper="store --file=/tmp/.gitcred" push origin HEAD:main
      '''
    }
  }
}
```

两个细节：

1. **token 不塞进 remote URL**。否则 `git push` 那行 `To https://user:token@github.com/...`
   会把凭据原样打在构建日志里。
2. **`HEAD:main` 而不是 `main`**。SCM checkout 出来是 detached HEAD，没有本地 `main` 分支。

防死循环：

- [ ] job 的 GitSCM 加 extension，正则 `\[skip ci\]`

git 插件 5.x 的类名是 **`MessageExclusion`**，字段 `excludedMessage`
（不是常说的 `MessageExcludeFilter`，那个在 5.x 已经不存在）。

配置方式：Job 配置页面 → Additional Behaviours → Polling ignores commits with certain messages。
或者 Script Console（`POST /job/x/config.xml` 整贴提交会 500，原样提交也 500，是接口问题）：

```groovy
def job = Jenkins.instance.getItemByFullName('<job名>')
job.definition.scm.extensions.replace(
  new hudson.plugins.git.extensions.impl.MessageExclusion('\\[skip ci\\]'))
job.save()
```

验收：push 一次正常代码 → 构建 → 回写 commit 出现 → **等 10 分钟，构建数不再增加**。

```bash
kubectl -n jenkins exec jenkins-0 -c jenkins -- \
  grep -A2 MessageExclusion /var/jenkins_home/jobs/<job名>/config.xml
```

---

## 8. 阶段 6：ArgoCD（约 1h）

- [ ] `helm repo add argo https://argoproj.github.io/argo-helm`
- [ ] 装精简版：关掉 dex / notifications / applicationSet，只留 controller + repoServer + server + redis
- [ ] `server.insecure=true` + NodePort 30081（省掉自签证书）
- [ ] 建 Application：

```yaml
spec:
  source:
    repoURL: https://github.com/<user>/<repo>.git
    targetRevision: main
    path: deploy
  destination:
    server: https://kubernetes.default.svc
    namespace: tls-monitor
  syncPolicy:
    automated: {prune: true, selfHeal: true}
```

- [ ] 初始 admin 密码在 `secret/argocd-initial-admin-secret`

验收：

```bash
kubectl -n argocd get application <name> -o jsonpath='{.status.sync.status} {.status.health.status}'
# 期望 Synced Healthy
kubectl -n tls-monitor get deploy -o jsonpath='{.items[0].spec.template.spec.containers[0].image}'
# 改 Git 里的 tag 后，这里应该 3 分钟内变化
```

> 坑：ApplicationSet controller 在没有 ApplicationSet CRD 时会 CrashLoop
> （`failed to wait for applicationset caches to sync`）。用不到就 scale 0。

---

## 9. 阶段 7：端到端验收（30min）

这是唯一能证明链路真的通的测试：

- [ ] 改 `main.go` 里任意一行注释
- [ ] `git push`，**之后全程不碰任何界面**
- [ ] 等 Jenkins 轮询（≤5min）自动构建
- [ ] 等 ArgoCD 轮询（≤3min）自动同步
- [ ] 检查 Pod 已重建、镜像 tag = 新 commit 前 7 位

```bash
kubectl -n tls-monitor get pod -o jsonpath='{.items[*].status.containerStatuses[*].image}'
```

全流程 4-8 分钟（热缓存 4 分钟，冷缓存 10 分钟）。

---

## 10. 八个坑速查

| 坑 | 症状 | 解法 |
|---|---|---|
| 插件下载卡死 | 日志停在某个数字不动 | 临时 Pod 预装 hpi，`installPlugins: []` |
| reload 不生效 | reload 完配置还是旧的 | 重启 Pod，sidecar 不扫新文件 |
| 两个同名 cloud | 模板不生效，Pod 起不来 | `defaultConfig: false` |
| 找不到配置 | ConfigMap 里没有自定义的 key | 每个 key 是独立 ConfigMap |
| golang:alpine 没 git | 回写 stage exit 127 | 脱离 `container('go')` |
| 类名不对 | `unable to resolve class` | 5.x 用 `MessageExclusion` |
| config.xml 500 | 原样提交也 500 | 走 Script Console 改 job 对象 |
| dpkg 锁被占 | 装包失败 | 停 unattended-upgrades |

---

## 11. 已知限制

以下是有意为之的取舍，不是遗漏。改生产环境前需要逐条处理。

- 单 master，无 HA（生产要 etcd 3 节点 + LB VIP + control plane 多副本）
- 轮询而非 webhook，最坏有 5 分钟延迟且浪费 API 调用
- 密码明文在 values 里（生产用 Sealed Secrets / External Secrets / Vault）
- 无镜像扫描和签名（Harbor + Trivy，镜像 cosign 签名 + admission 校验）
- 无灰度与回滚策略（现在只能 `git revert`）
- 全链路单点：node1 挂了 Harbor 和 NFS 一起没了
- 资源限制靠手动 patch，应该全部走 values 和 ResourceQuota
