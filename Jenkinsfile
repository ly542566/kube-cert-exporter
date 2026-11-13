pipeline {
  agent { label 'go' }

  triggers { pollSCM('H/5 * * * *') }

  environment {
    REGISTRY = '172.25.58.116:30002'
    IMAGE    = 'library/kube-cert-exporter'
    TAG      = "${env.GIT_COMMIT ? env.GIT_COMMIT.take(7) : 'b' + env.BUILD_NUMBER}"
  }

  stages {
    stage('Test') {
      steps {
        container('go') {
          sh '''
            export GOPROXY=https://mirrors.aliyun.com/goproxy/,https://goproxy.cn,direct
            export GOSUMDB=off
            go vet ./...
            go test ./... -count=1
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
