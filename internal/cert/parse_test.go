package cert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"testing"
	"time"
)

// makeCertPEM 造一张自签证书，返回 PEM 字节。
// 它只是「准备测试数据」，本身不是测试用例，所以不以 Test 开头。
func makeCertPEM(t *testing.T, cn string, notBefore, notAfter time.Time) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成私钥失败: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		DNSNames:              []string{cn},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("签发证书失败: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// 表驱动测试：Go 里最标准的写法。
// 好处是一眼能看全所有边界，加用例只加一行数据，
// 而且失败信息里带用例名，不用猜是哪个分支挂的。
//
// 这个测试还顺带钉住了 CN 的回退行为：
// Subject.CN 为空时取第一个 SAN，因为 cert-manager 这类工具签出来的证书
// 身份信息全在 SAN 里，只读 CN 会拿到空标签。
func TestParse(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		data    []byte
		wantErr bool
		wantCN  string
	}{
		{
			name:   "正常证书",
			data:   makeCertPEM(t, "example.com", now.Add(-time.Hour), now.Add(90*24*time.Hour)),
			wantCN: "example.com",
		},
		{
			// makeCertPEM 会同时写 CN 和 DNSNames，
			// 所以这里直接构造一张只有 SAN、没有 CN 的证书
			name:   "只有 SAN 没有 CN",
			data:   makeCertPEMSANOnly(t, "svc.cluster.local", now.Add(-time.Hour), now.Add(24*time.Hour)),
			wantCN: "svc.cluster.local",
		},
		{
			name:    "不是 PEM",
			data:    []byte("this is not a certificate"),
			wantErr: true,
		},
		{
			name:    "空数据",
			data:    nil,
			wantErr: true,
		},
		{
			name: "私钥而不是证书",
			data: pem.EncodeToMemory(&pem.Block{
				Type:  "PRIVATE KEY",
				Bytes: []byte("fake"),
			}),
			wantErr: true,
		},
		{
			name:    "PEM 头正确但内容不是 DER",
			data:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage")}),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := Parse(tc.data)

			// 先判错，再判字段：出错时 info 是 nil，往下读会 panic
			if tc.wantErr {
				if err == nil {
					t.Fatal("期望报错，但解析成功了")
				}
				return
			}
			if err != nil {
				t.Fatalf("不期望报错: %v", err)
			}
			if info.CommonName != tc.wantCN {
				t.Errorf("CommonName = %q，期望 %q", info.CommonName, tc.wantCN)
			}
		})
	}
}

// makeCertPEMSANOnly 造一张 Subject.CN 为空、只有 SAN 的证书，
// 模拟 cert-manager 的常见产物。
func makeCertPEMSANOnly(t *testing.T, san string, notBefore, notAfter time.Time) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成私钥失败: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: ""}, // 刻意留空
		DNSNames:              []string{san},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("签发证书失败: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// 用例 2：已过期的证书，Parse 仍应解析成功
func TestParseExpired(t *testing.T) {
	now := time.Now()
	data := makeCertPEM(t, "old.example.com", now.Add(-100*24*time.Hour), now.Add(-10*24*time.Hour))

	info, err := Parse(data)
	if err != nil {
		t.Fatalf("解析过期证书不该报错，Parse 只管解析不管有效期: %v", err)
	}

	if !info.NotAfter.Before(now) {
		t.Errorf("NotAfter = %s，期望是过去的时间", info.NotAfter.Format(time.RFC3339))
	}
}

// Expired 的判断：用表驱动表达「临界值」这一组语义。
// 这里刻意不用 DaysLeft 做断言，因为 DaysLeft 会整数截断：
// 「还剩 3 小时」算出来是 0 天，用 0 当边界会漏掉最危险的情况。
func TestExpired(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		notAfter time.Time
		want     bool
	}{
		{name: "10 天后到期", notAfter: now.Add(10 * 24 * time.Hour), want: false},
		{name: "还剩 3 小时（整数截断会算成 0 天）", notAfter: now.Add(3 * time.Hour), want: false},
		{name: "5 天前已过期", notAfter: now.Add(-5 * 24 * time.Hour), want: true},
		{name: "1 分钟前刚过期", notAfter: now.Add(-time.Minute), want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := makeCertPEM(t, "case.example.com", now.Add(-30*24*time.Hour), tc.notAfter)
			info, err := Parse(data)
			if err != nil {
				t.Fatalf("造数据失败: %v", err)
			}
			if got := info.Expired(now); got != tc.want {
				t.Errorf("Expired = %v，期望 %v（NotAfter = %s）",
					got, tc.want, info.NotAfter.Format(time.RFC3339))
			}
		})
	}
}

// 用例 5：拿真实证书跑一遍，顺便和 openssl 对账
func TestParseRealFile(t *testing.T) {
	data, err := os.ReadFile("testdata/real.pem")
	if err != nil {
		t.Skip("没有 testdata/real.pem，跳过")
	}

	info, err := Parse(data)
	if err != nil {
		t.Fatalf("解析真实证书失败: %v", err)
	}

	t.Logf("CN=%s", info.CommonName)
	t.Logf("Issuer=%s", info.Issuer)
	t.Logf("DNSNames=%v", info.DNSNames)
	t.Logf("NotBefore=%s", info.NotBefore.Format(time.RFC3339))
	t.Logf("NotAfter=%s", info.NotAfter.Format(time.RFC3339))
}
