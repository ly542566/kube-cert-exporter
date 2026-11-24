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

// 大量 Secret 时，逐条核对 namespace/name 与证书内容是否对应。
//
// 这条断言原来长在并发测试里，并发删掉后它必须留下：
// 它验证的是「结果不能张冠李戴」——数量对但 A 的证书挂到 B 名下，
// 是最难靠肉眼发现的错误，而指标标签错了会让告警指向错误的 Secret。
// 跟并发与否无关，是 Collect 本身的契约。
func TestCollectManySecretsNoCrossTalk(t *testing.T) {
	goodPEM := readGoodPEM(t)

	const count = 60

	// 名字和 namespace 里的编号是绑定的，用来检测串位：
	// good-07 必须落在 ns-07 里。
	wantNS := make(map[string]string, count)
	objs := make([]runtime.Object, 0, count)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("good-%02d", i)
		ns := fmt.Sprintf("ns-%02d", i)
		wantNS[name] = ns
		objs = append(objs, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{"tls.crt": goodPEM},
		})
	}

	c := collect.New(fake.NewClientset(objs...))
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("不期望报错：%v", err)
	}

	if len(res.Certs) != count {
		t.Fatalf("拿到 %d 张证书，期望 %d 张", len(res.Certs), count)
	}
	if len(res.Failures) != 0 {
		t.Fatalf("不期望有失败，实际 %d 个", len(res.Failures))
	}

	seen := make(map[string]bool, count)
	for _, ci := range res.Certs {
		ns, ok := wantNS[ci.SecretName]
		if !ok {
			t.Errorf("出现了未预期的 Secret %q", ci.SecretName)
			continue
		}
		if ci.Namespace != ns {
			t.Errorf("Secret %q 的 namespace = %q，期望 %q（结果串到了别的条目上）",
				ci.SecretName, ci.Namespace, ns)
		}
		// real.pem 的 CN 是 localhost；若解析结果被覆写，这里会读到别的值
		if ci.CommonName != "localhost" {
			t.Errorf("Secret %q 的 CN = %q，期望 localhost", ci.SecretName, ci.CommonName)
		}
		if seen[ci.SecretName] {
			t.Errorf("Secret %q 出现了两次", ci.SecretName)
		}
		seen[ci.SecretName] = true
	}
	if len(seen) != count {
		t.Errorf("去重后只剩 %d 个 Secret，期望 %d 个", len(seen), count)
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
