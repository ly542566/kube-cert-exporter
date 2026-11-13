pipeline {
  agent { label 'go' }

  triggers { pollSCM('H/5 * * * *') }

  environment {
    REGISTRY = '172.25.58.116:30002'
    IMAGE    = 'library/kube-cert-exporter'
    TAG      = "${env.GIT_COMMIT ? env.GIT_COMMIT.take(7) : 'b' + env.BUILD_NUMBER}"
    // 编译缓存放宿主机（模板里的 hostPathVolume 挂到 /gocache），跨构建复用，
    // 否则 go vet 每次都要把 vendor 里几百个包重新编一遍
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
  }
}
