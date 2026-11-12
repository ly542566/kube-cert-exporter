package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lart5/kube-cert-exporter/internal/collect"
	"github.com/lart5/kube-cert-exporter/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// 单次采集的超时上限。必须小于 ServiceMonitor 里的 scrapeTimeout（30s），
// 否则 Prometheus 早已放弃连接，这边还在白烧 API 配额。
const collectTimeout = 15 * time.Second

// 收到 SIGTERM 后等待在途请求结束的时间。
// 必须小于 Pod 的 terminationGracePeriodSeconds（默认 30s），
// 不然 kubelet 会等不及直接 SIGKILL，优雅退出就白做了。
const shutdownTimeout = 10 * time.Second

func main() {
	addr := flag.String("addr", ":8080", "HTTP 监听地址")
	flag.Parse()

	// JSON 格式的结构化日志：Loki / ES / 云日志服务都能直接按字段检索。
	// 命令行里 `kubectl logs` 看会略丑，但线上排查时能按 level、err 精确过滤。
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if err := run(logger, *addr); err != nil {
		logger.Error("进程退出", "err", err)
		os.Exit(1)
	}
}

// run 把真正的工作逻辑与 os.Exit 分离，这样 defer 才能正常执行。
// 直接在 main 里调 log.Fatal 的话，所有 defer 都会被跳过。
func run(logger *slog.Logger, addr string) error {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("读取集群内配置失败（确认 Pod 已挂载 ServiceAccount）: %w", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("创建 kubernetes client 失败: %w", err)
	}

	collector := collect.New(client)
	m := metrics.New(prometheus.DefaultRegisterer)
	promHandler := promhttp.Handler()

	// 显式建一个 mux，不碰 http.DefaultServeMux。
	// 全局 mux 是进程级单例，任何被引入的库都能往里注册路由，
	// 而且测试之间会互相污染（httptest 用同名路径会 panic）。
	mux := http.NewServeMux()

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), collectTimeout)
		defer cancel()

		res, err := collector.Collect(ctx)
		if err != nil {
			// 区分「对端已经走了」和「真的出错了」：
			// 前者是 Prometheus 超时断开，记 warn 就够，不需要惊动值班；
			// 后者才是需要人看的问题。
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				logger.Warn("采集未完成，请求已取消", "err", err)
			} else {
				logger.Error("采集失败", "err", err)
			}
			// 返回 500 会让 up=0，触发 KubeCertExporterDown 告警。
			// 注意此时不调用 m.Update，指标保留上一次的完整结果，
			// 避免把「采集中断」误报成「证书全都消失了」。
			http.Error(w, "采集失败", http.StatusInternalServerError)
			return
		}

		m.Update(res)
		promHandler.ServeHTTP(w, r)
	})

	// 给 kubelet 的探针一个轻量端点：不查 K8s API，只证明进程活着。
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
		// 防慢速攻击：只连不发 header 的连接最多占 5 秒。
		// ListenAndServe 的默认值是无限等待，一个僵死连接能一直占着 goroutine。
		ReadHeaderTimeout: 5 * time.Second,
	}

	// NotifyContext 在收到 SIGINT/SIGTERM 时取消 ctx，
	// 把「信号」统一成「context 取消」这一种控制流，不用自己写信号通道。
	// K8s 删除 Pod 时发的正是 SIGTERM。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("HTTP 服务启动", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// 两个等待条件：要么服务自己挂了，要么收到退出信号。
	// 不能用阻塞的 ListenAndServe 放在最后——那样永远走不到关闭逻辑。
	select {
	case err := <-serverErr:
		return fmt.Errorf("HTTP 服务异常: %w", err)
	case <-ctx.Done():
		logger.Info("收到退出信号，开始优雅关闭")
	}

	// 这里必须用全新的 context：ctx 已经因为收到信号而被取消了，
	// 拿它当 Shutdown 的超时控制会立刻返回，等于没等。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("优雅关闭超时: %w", err)
	}

	logger.Info("已退出")
	return nil
}
