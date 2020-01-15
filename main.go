package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"

	"github.com/cmattoon/conntrackr/conntrack"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/zjj2wry/conntrack-exporter/controller"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	nfConntrackMax   = "/proc/sys/net/netfilter/nf_conntrack_max"
	nfConntrackCount = "/proc/sys/net/netfilter/nf_conntrack_count"
	nfConntrackStat  = "/proc/net/stat/nf_conntrack"
	nfConntrackList  = "/proc/net/nf_conntrack"
)

var (
	kubeconfig  = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	statMetrics = []string{
		"entries",
		"searched",
		"found",
		"new",
		"invalid",
		"ignore",
		"delete",
		"delete_list",
		"insert",
		"insert_failed",
		"drop",
		"early_drop",
		"icmp_error",
		"expect_new",
		"expect_create",
		"expect_delete",
		"search_restart"}

	nodeNfConntrackMax = prometheus.NewDesc(
		"node_nf_conntrack_max",
		"",
		[]string{"node"},
		nil,
	)

	nodeNfConntrackCount = prometheus.NewDesc(
		"node_nf_conntrack_count",
		"",
		[]string{"node"},
		nil,
	)

	nodeNfConntrackList = prometheus.NewDesc(
		"node_nf_conntrack_entrylist",
		"",
		[]string{"src", "des", "state", "assured", "protocal", "node", "src_namespace", "src_kind", "des_namespace", "des_kind", "src_ip", "des_ip"},
		nil,
	)
)

func main() {
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		panic("node name can not be empty, NODE_NAME environment variable should be passed through kubernetes downward api")
	}
	flag.Parse()

	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	factory := informers.NewSharedInformerFactory(clientset, 0)
	ep := factory.Core().V1().Endpoints()
	node := factory.Core().V1().Nodes()
	svc := factory.Core().V1().Services()

	stop := make(chan struct{})

	ctrl := controller.NewIPAliasController(ep, svc, node)
	go ctrl.Run(1, stop)
	factory.Start(stop)

	statMetricsMap := make(map[string]*prometheus.Desc, 0)
	for _, metricsName := range statMetrics {
		statMetricsMap[metricsName] = prometheus.NewDesc(
			fmt.Sprintf("node_nf_conntrack_stat_%s", metricsName),
			"",
			[]string{"node", "cpu"},
			nil,
		)
	}

	ctk := &Conntrack{
		NodeName:    nodeName,
		StatMetrics: statMetricsMap,
		Ctrl:        ctrl,
	}

	prometheus.MustRegister(ctk)
	server := http.NewServeMux()
	server.Handle("/metrics", promhttp.Handler())
	server.HandleFunc("/debug/pprof/", pprof.Index)
	server.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	server.HandleFunc("/debug/pprof/profile", pprof.Profile)
	server.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	server.HandleFunc("/debug/pprof/trace", pprof.Trace)
	err = http.ListenAndServe(":10086", server)
	if err != nil {
		panic(err)
	}
}

type Conntrack struct {
	NodeName    string
	StatMetrics map[string]*prometheus.Desc
	Ctrl        *controller.IPAliasController
}

func (o *Conntrack) Describe(ch chan<- *prometheus.Desc) {
	ch <- nodeNfConntrackMax
	ch <- nodeNfConntrackCount
	ch <- nodeNfConntrackList
	for _, des := range o.StatMetrics {
		ch <- des
	}
}

type label struct {
	Src      string
	Dst      string
	Protocal string
	State    string
	Assured  string
}

func (o *Conntrack) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(nodeNfConntrackMax, prometheus.GaugeValue,
		float64(conntrack.GetUint32FromFile(nfConntrackMax)), o.NodeName)
	ch <- prometheus.MustNewConstMetric(nodeNfConntrackCount, prometheus.GaugeValue,
		float64(conntrack.GetUint32FromFile(nfConntrackCount)), o.NodeName)

	res, err := conntrack.Stat(nfConntrackStat)
	if err != nil {
		log.Fatalln(err)
	}

	for _, stat := range res.Items {
		statMap := toMap(stat)
		for name, des := range o.StatMetrics {
			ch <- prometheus.MustNewConstMetric(des, prometheus.CounterValue,
				statMap[name], o.NodeName, strconv.Itoa(stat.Id))
		}
	}

	entryList, err := conntrack.GetConnections(nfConntrackList)
	if err != nil {
		log.Fatalln(err)
	}

	entryMap := make(map[label][]conntrack.Entry, 0)
	for _, entry := range entryList.Items {
		key := label{
			Src:      entry.Outbound.Src.Addr,
			Dst:      entry.Outbound.Dst.Addr,
			State:    entry.State,
			Assured:  strconv.FormatBool(entry.IsAssured),
			Protocal: entry.TxProto,
		}
		entryMap[key] = append(entryMap[key], *entry)
	}

	for label, entries := range entryMap {
		srcRes := o.Ctrl.Get(label.Src)
		src := label.Src
		srcNamespace := ""
		srcKind := ""
		if srcRes != nil {
			src = srcRes.Name
			srcNamespace = srcRes.Namespace
			srcKind = srcRes.Kind
		}
		dstRes := o.Ctrl.Get(label.Dst)
		dst := label.Dst
		dstNamespace := ""
		dstKind := ""
		if dstRes != nil {
			dst = dstRes.Name
			dstNamespace = dstRes.Namespace
			dstKind = dstRes.Kind
		}
		ch <- prometheus.MustNewConstMetric(nodeNfConntrackList, prometheus.GaugeValue,
			float64(len(entries)), src, dst, label.State, label.Assured, label.Protocal, o.NodeName, srcNamespace, srcKind, dstNamespace, dstKind, label.Src, label.Dst)
	}
}

func toMap(stat *conntrack.StatResult) map[string]float64 {
	statMap := make(map[string]float64, 0)

	statMap["entries"] = float64(stat.Entries)
	statMap["searched"] = float64(stat.Searched)
	statMap["found"] = float64(stat.Found)
	statMap["new"] = float64(stat.New)
	statMap["invalid"] = float64(stat.Invalid)
	statMap["ignore"] = float64(stat.Ignore)
	statMap["delete"] = float64(stat.Delete)
	statMap["delete_list"] = float64(stat.DeleteList)
	statMap["insert"] = float64(stat.Insert)
	statMap["insert_failed"] = float64(stat.InsertFailed)
	statMap["drop"] = float64(stat.Drop)
	statMap["early_drop"] = float64(stat.EarlyDrop)
	statMap["icmp_error"] = float64(stat.IcmpError)
	statMap["expect_new"] = float64(stat.ExpectNew)
	statMap["expect_create"] = float64(stat.ExpectCreate)
	statMap["expect_delete"] = float64(stat.ExpectDelete)
	statMap["search_restart"] = float64(stat.SearchRestart)
	return statMap
}
