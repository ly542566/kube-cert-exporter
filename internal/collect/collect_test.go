package collect_test

import (
	"context"
	"os"
	"testing"

	"github.com/lart5/kube-cert-exporter/internal/collect"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCollect(t *testing.T) {
	// 拿一张真证书当「好数据」，路径相对于 internal/collect 这个包目录
	goodPEM, err := os.ReadFile("../cert/testdata/real.pem")
	if err != nil {
		t.Fatalf("读测试证书失败: %v", err)
	}

	client := fake.NewClientset(
		// ① 正常的 TLS Secret
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "good-tls", Namespace: "default"},
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{"tls.crt": goodPEM, "tls.key": []byte("fake")},
		},
		// ② 类型是 TLS，但内容不是证书
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "broken-tls", Namespace: "prod"},
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{"tls.crt": []byte("这不是证书")},
		},
		// ③ 普通 Secret，类型不对，应该被过滤
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "default"},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"foo": []byte("bar")},
		},
	)

	c := collect.New(client)
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("不期望报错：%v", err)
	}

	if len(res.Certs) != 1 {
		t.Fatalf("拿到 %d 张证书，期望 1 张", len(res.Certs))
	}

	if len(res.Failures) != 1 {
		t.Fatalf("失败 %d 个，期望 1 个", len(res.Failures))
	}

	if res.Failures[0].SecretName != "broken-tls" {
		t.Errorf("失败的 Secret = %q，期望 broken-tls", res.Failures[0].SecretName)
	}

	t.Logf("失败的 Secret: %s", res.Failures[0])

	if res.Certs[0].Namespace != "default" || res.Certs[0].SecretName != "good-tls" {
		t.Errorf("namespace/name = %s/%s，期望 default/good-tls",
			res.Certs[0].Namespace, res.Certs[0].SecretName)
	}

	if res.Certs[0].CommonName != "localhost" {
		t.Errorf("CN = %q，期望 localhost", res.Certs[0].CommonName)
	}
}
