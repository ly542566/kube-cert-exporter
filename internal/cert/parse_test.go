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

// 用例 1：正常证书，CN 能正确读出来
func TestParseValid(t *testing.T) {
	now := time.Now()
	data := makeCertPEM(t, "example.com", now.Add(-24*time.Hour), now.Add(90*24*time.Hour))

	info, err := Parse(data)
	if err != nil {
		t.Fatalf("不期望报错: %v", err)
	}

	if info.CommonName != "example.com" {
		t.Errorf("CommonName = %q，期望 example.com", info.CommonName)
	}
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

// 用例 3：不是 PEM 的数据，应该报错
func TestParseNotPEM(t *testing.T) {
	_, err := Parse([]byte("this is not a certificate"))
	if err == nil {
		t.Fatal("期望报错，但解析成功了")
	}
}

// 用例 4：Expired 的判断
func TestExpired(t *testing.T) {
	now := time.Now()

	// 情况一：10 天后到期，不该判定为过期
	valid := makeCertPEM(t, "valid.example.com", now.Add(-24*time.Hour), now.Add(10*24*time.Hour))
	info, err := Parse(valid)
	if err != nil {
		t.Fatalf("造数据失败: %v", err)
	}
	if info.Expired(now) {
		t.Error("10 天后到期的证书不该判定为过期")
	}

	// 情况二：TODO 你来补
	// 造一张「5 天前就已经过期」的证书（NotBefore 和 NotAfter 都填过去的时间），
	// 断言 info.Expired(now) 返回 true。
	// 用 makeCertPEM + Parse，照抄上面的写法。
	expired := makeCertPEM(t, "expire.example.com", now.Add(-30*24*time.Hour), now.Add(-5*24*time.Hour))
	expiredinfo, err := Parse(expired)
	if err != nil {
		t.Fatalf("造数据失败：%v", err)
	}
	if !expiredinfo.Expired(now) {
		t.Errorf("5 天前到期的证书应该判定为过期，NotAfter = %s", expiredinfo.NotAfter.Format(time.RFC3339))
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

func TestParsePrivateKey(t *testing.T) {
	// ① 造一个「私钥」形状的 PEM 块（内容是假的，无所谓，会被类型检查挡住）
	data := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: []byte("fake"),
	})

	// ② 调用
	_, err := Parse(data)

	// ③ 断言：应该报错
	if err == nil {
		t.Fatal("把私钥传给 Parse 应该报错")
	}

	// 再进一步：确认错误信息里提到了 CERTIFICATE，而不是笼统的错误
	t.Logf("实际错误: %v", err)
}
