pipeline {
  agent { label 'go' }

  options {
    disableConcurrentBuilds()
  }

  triggers { pollSCM('H/5 * * * *') }

  environment {
    REGISTRY = '172.25.58.116:30002'
    IMAGE    = 'library/kube-cert-exporter'
    TAG      = "${env.GIT_COMMIT ? env.GIT_COMMIT.take(7) : 'b' + env.BUILD_NUMBER}"
    // 编译缓存放宿主机（Pod 模板里的 hostPathVolume 挂到 /gocache），跨构建复用。
    // 没有缓存时 go vet 每次都要把 vendor 里几百个包重新编译一遍（约 5 分钟）
    GOCACHE  = '/gocache'
  }

  stages {
    stage('Test') {
      steps {
        container('go') {
          sh '''
            go vet -mod=vendor ./...
            go test -mod=vendor ./... -count=1
            mkdir -p bin
            CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags="-s -w" -o bin/exporter .
          '''
        }
      }
    }

    stage('Build & Push') {
      steps {
        container('kaniko') {
          withCredentials([usernamePassword(
            credentialsId: 'harbor-cred',
            usernameVariable: 'HARBOR_USER',
            passwordVariable: 'HARBOR_PASS'
          )]) {
            sh '''
              mkdir -p /kaniko/.docker
              AUTH=$(printf "%s:%s" "$HARBOR_USER" "$HARBOR_PASS" | base64 | tr -d "\\n")
              printf '{"auths":{"%s":{"auth":"%s"}}}' "$REGISTRY" "$AUTH" > /kaniko/.docker/config.json
              /kaniko/executor \
                --context=dir://$WORKSPACE \
                --dockerfile=$WORKSPACE/Dockerfile \
                --destination=$REGISTRY/$IMAGE:$TAG \
                --insecure --skip-tls-verify
            '''
          }
        }
      }
    }

    // GitOps 最后一环：把新 tag 写回 deploy/20-deployment.yaml 再 push，
    // ArgoCD 轮询到新 commit 后自动把集群镜像换成刚构建出来的这个。
    // 提交信息带 [skip ci]，job 的 SCM 配了 MessageExclusion 过滤器，
    // 这条回写 commit 不会再次触发构建，不会形成死循环。
    stage('Update Git') {
      steps {
        container('go') {
          withCredentials([usernamePassword(
            credentialsId: 'github-pat',
            usernameVariable: 'GH_USER',
            passwordVariable: 'GH_TOKEN'
          )]) {
            sh '''
              set -eu
              FILE=deploy/20-deployment.yaml
              NEW_IMAGE="$REGISTRY/$IMAGE:$TAG"

              # token 走 credential helper，不塞进 remote URL，免得 git 把地址回显到构建日志
              printf 'https://%s:%s@github.com\\n' "$GH_USER" "$GH_TOKEN" > /tmp/.gitcred
              trap 'rm -f /tmp/.gitcred' EXIT
              GITC="credential.helper=store --file=/tmp/.gitcred"

              sed -i "s|^\\( *image: \\).*kube-cert-exporter:.*|\\1${NEW_IMAGE}|" "$FILE"
              grep -qF "$NEW_IMAGE" "$FILE" || { echo "回写失败：$FILE 中未找到 $NEW_IMAGE"; exit 1; }

              if git diff --quiet -- "$FILE"; then
                echo "tag 未变化（$NEW_IMAGE），跳过回写"
                exit 0
              fi

              git add -- "$FILE"
              git -c user.name="jenkins-ci" \
                  -c user.email="jenkins-ci@users.noreply.github.com" \
                  commit -m "ci: bump kube-cert-exporter to $TAG [skip ci]"

              git -c "$GITC" push origin HEAD:main
              echo "已回写：$NEW_IMAGE"
            '''
          }
        }
      }
    }
  }
}
