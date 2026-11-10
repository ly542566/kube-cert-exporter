package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/lart5/kube-cert-exporter/internal/collect"
	"github.com/lart5/kube-cert-exporter/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP 监听地址")
	flag.Parse()

	cfg, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
	if err != nil {
		log.Fatalf("读取 kubeconfig 失败: %v", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("创建 client 失败: %v", err)
	}

	collector := collect.New(client)
	m := metrics.New(prometheus.DefaultRegisterer)
	promHandler := promhttp.Handler()

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		res, err := collector.Collect(ctx)
		if err != nil {
			log.Printf("采集失败: %v", err)
			http.Error(w, "采集失败", http.StatusInternalServerError)
			return
		}

		m.Update(res)
		promHandler.ServeHTTP(w, r)
	})

	log.Printf("启动成功，指标地址 http://localhost%s/metrics", *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}
