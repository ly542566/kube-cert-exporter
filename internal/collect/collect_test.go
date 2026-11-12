package collect_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/lart5/kube-cert-exporter/internal/collect"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func readGoodPEM(t *testing.T) []byte {
	t.Helper()
	// 路径相对于 internal/collect 这个包目录
	data, err := os.ReadFile("../cert/testdata/real.pem")
	if err != nil {
		t.Fatalf("读测试证书失败: %v", err)
	}
	return data
}

func TestCollect(t *testing.T) {
	goodPEM := readGoodPEM(t)

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

// 并发正确性的核心测试：
// 大量 Secret 并发解析时，不能丢、不能串、不能因为竞争崩溃。
// 配合 `go test -race` 跑，能直接抓出对共享 slice 的竞争。
func TestCollectConcurrentManySecrets(t *testing.T) {
	goodPEM := readGoodPEM(t)

	const goodCount = 60
	const badCount = 20

	var objs []runtime.Object
	for i := 0; i < goodCount; i++ {
		objs = append(objs, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("good-%02d", i), Namespace: "default"},
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{"tls.crt": goodPEM},
		})
	}
	for i := 0; i < badCount; i++ {
		objs = append(objs, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("bad-%02d", i), Namespace: "prod"},
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{"tls.crt": []byte("not a cert")},
		})
	}

	client := fake.NewClientset(objs...)

	// 并发度故意设成 4，让队列真的排起来，而不是一把全放出去
	c := collect.New(client, collect.WithWorkers(4))
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("不期望报错：%v", err)
	}

	if len(res.Certs) != goodCount {
		t.Errorf("拿到 %d 张证书，期望 %d 张", len(res.Certs), goodCount)
	}
	if len(res.Failures) != badCount {
		t.Errorf("失败 %d 个，期望 %d 个", len(res.Failures), badCount)
	}

	// 光比数量不够：按下标写入时，如果写错位置会出现
	// 「数量对但内容串了」的情况（A 的证书挂到 B 的名下）。
	// 所以逐个核对 namespace/name 是否和证书内容对应。
	seen := make(map[string]bool, len(res.Certs))
	for _, ci := range res.Certs {
		if ci.Namespace != "default" {
			t.Errorf("证书 %q 的 namespace = %q，期望 default", ci.SecretName, ci.Namespace)
		}
		// real.pem 的 CN 是 localhost；如果串了位置，这里会读到别的值
		if ci.CommonName != "localhost" {
			t.Errorf("证书 %q 的 CN = %q，期望 localhost", ci.SecretName, ci.CommonName)
		}
		if seen[ci.SecretName] {
			t.Errorf("Secret %q 出现了两次，说明按索引写入串了位置", ci.SecretName)
		}
		seen[ci.SecretName] = true
	}
}

// 单并发也要能跑完：验证限并发逻辑本身没有死锁。
// 把 workers 设成 1 是最容易暴露「信号量获取/释放不对称」的配置。
func TestCollectWorkersOne(t *testing.T) {
	goodPEM := readGoodPEM(t)
	client := fake.NewClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "tls-a", Namespace: "default"},
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{"tls.crt": goodPEM},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "tls-b", Namespace: "default"},
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{"tls.crt": goodPEM},
		},
	)

	c := collect.New(client, collect.WithWorkers(1))
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("不期望报错：%v", err)
	}
	if len(res.Certs) != 2 {
		t.Errorf("拿到 %d 张证书，期望 2 张", len(res.Certs))
	}
}

// 请求被取消时，必须返回错误而不是「残缺结果」。
// 如果照样返回，零值时间的证书会算出负一千多万天的剩余天数，直接污染指标。
func TestCollectCanceledContext(t *testing.T) {
	goodPEM := readGoodPEM(t)
	client := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tls-a", Namespace: "default"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": goodPEM},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 进来之前就已经取消

	c := collect.New(client)
	res, err := c.Collect(ctx)
	if err == nil {
		t.Fatalf("ctx 已取消时应该返回错误，实际拿到了 %d 张证书", len(res.Certs))
	}
}
