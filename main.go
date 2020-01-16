package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"time"

	"github.com/cmattoon/conntrackr/conntrack"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	conntrack2 "github.com/ti-mo/conntrack"
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
	kubeconfig           = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	contrackReadBufSize  = flag.Int("conntrack-read-buf-size", 4086*1024, "conntrack read buf size")
	contrackEventBufSize = flag.Int64("conntrack-event-buf-size", 1024*1024, "conntrack event buf size")
	contrackEventWorker  = flag.Int("conntrack-event-worker", 1, "conntrack event worker")

	tcpState = []string{
		"NONE",
		"SYN_SENT",
		"SYN_RECV",
		"ESTABLISHED",
		"FIN_WAIT",
		"CLOSE_WAIT",
		"LAST_ACK",
		"TIME_WAIT",
		"CLOSE",
		"LISTEN",
		"MAX",
		"IGNORE",
	}

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

	nodeNfConntrackList = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "node",
			Subsystem: "nf_conntrack",
			Name:      "entrylist",
		},
		[]string{"src", "des", "protocal", "node", "src_namespace", "src_kind", "des_namespace", "des_kind", "src_ip", "des_ip", "state"},
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

	// ctk.collectConntrackList()
	go func() {
		for {
			ctk.event()
			time.Sleep(120 * time.Second)
		}
	}()

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
	nodeNfConntrackList.Describe(ch)
	ch <- nodeNfConntrackMax
	ch <- nodeNfConntrackCount
	for _, des := range o.StatMetrics {
		ch <- des
	}
}

type label struct {
	Src      string
	Dst      string
	Protocal string
	State    string
}

func (o *Conntrack) Collect(ch chan<- prometheus.Metric) {
	now := time.Now()
	defer func() {
		fmt.Printf("collect nfconntrack stat end(%v)\n", time.Since(now))
	}()
	ch <- prometheus.MustNewConstMetric(nodeNfConntrackMax, prometheus.GaugeValue,
		float64(conntrack.GetUint32FromFile(nfConntrackMax)), o.NodeName)
	ch <- prometheus.MustNewConstMetric(nodeNfConntrackCount, prometheus.GaugeValue,
		float64(conntrack.GetUint32FromFile(nfConntrackCount)), o.NodeName)
	o.collectConntrackStat(ch)
	// Use asynchronous method, otherwise promethus may call / metrics timeout
	nodeNfConntrackList.Collect(ch)
}

func (o *Conntrack) collectConntrackList() {
	now := time.Now()
	defer func() {
		fmt.Printf("collect conntrack list end(%v)\n", time.Since(now))
	}()
	entryList, err := conntrack.GetConnections(nfConntrackList)
	if err != nil {
		log.Fatalln(err)
	}

	fmt.Printf("contrack connections length(%v)\n", len(entryList.Items))

	entryMap := make(map[label][]conntrack.Entry, 0)
	for _, entry := range entryList.Items {
		key := label{
			Src:      entry.Outbound.Src.Addr,
			Dst:      entry.Outbound.Dst.Addr,
			State:    entry.State,
			Protocal: entry.TxProto,
		}
		entryMap[key] = append(entryMap[key], *entry)
	}

	for label, entries := range entryMap {
		nodeNfConntrackList.WithLabelValues(o.conntrackListLabels(label.Src, label.Dst, label.Protocal)...).Set(float64(len(entries)))
	}
}

func (o *Conntrack) conntrackListLabels(srcIP, dstIP, protocal string) []string {
	srcRes := o.Ctrl.Get(srcIP)
	src := srcIP
	srcNamespace := ""
	srcKind := ""
	if srcRes != nil {
		src = srcRes.Name
		srcNamespace = srcRes.Namespace
		srcKind = srcRes.Kind
	}
	dstRes := o.Ctrl.Get(dstIP)
	dst := dstIP
	dstNamespace := ""
	dstKind := ""
	if dstRes != nil {
		dst = dstRes.Name
		dstNamespace = dstRes.Namespace
		dstKind = dstRes.Kind
	}
	return []string{src, dst, protocal, o.NodeName, srcNamespace, srcKind, dstNamespace, dstKind, srcIP, dstIP}
}

func (o *Conntrack) collectConntrackStat(ch chan<- prometheus.Metric) {
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

func (o *Conntrack) event() {
	now := time.Now()
	defer func() {
		fmt.Printf("list all contrack(%v)\n", time.Since(now))
	}()

	// Open a Conntrack connection.
	c, err := conntrack2.Dial(nil)
	if err != nil {
		log.Fatal(err)
	}

	err = c.SetReadBuffer(*contrackReadBufSize)
	if err != nil {
		log.Fatal(err)
	}

	flows, err := c.Dump()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("contrack flow length(%v)\n", len(flows))
	entryMap := make(map[label][]conntrack2.Flow, 0)
	for _, flow := range flows {
		key := label{
			Src:      flow.TupleOrig.IP.SourceAddress.String(),
			Dst:      flow.TupleOrig.IP.DestinationAddress.String(),
			Protocal: protoLookup(flow.TupleOrig.Proto.Protocol),
		}
		if flow.ProtoInfo.TCP != nil {
			key.State = tcpState[flow.ProtoInfo.TCP.State]
		}

		entryMap[key] = append(entryMap[key], flow)
	}

	for label, entries := range entryMap {
		nodeNfConntrackList.WithLabelValues(append(o.conntrackListLabels(label.Src, label.Dst, label.Protocal), label.State)...).Set(float64(len(entries)))
	}

	// TODO: how watch event?
	// The prd environment has 14w + conntrack entries. When watching events,
	// it will eat a lot of cpu(200m ~ 1core), and it needs to be restarted continuously, otherwise it will fill up buf size.

	// for _,flow := range flows{
	// 	src := e.Flow.TupleOrig.IP.SourceAddress.String()
	// 	des := e.Flow.TupleOrig.IP.DestinationAddress.String()
	// 	protocal := protoLookup(e.Flow.TupleOrig.Proto.Protocol)
	// 	nodeNfConntrackList.WithLabelValues(o.conntrackListLabels(src, des, protocal)...).Inc()
	// }
	/*
		// Listen to Conntrack events from all network namespaces on the system.
		err = c.SetOption(netlink.ListenAllNSID, true)
		if err != nil {
			log.Fatal(err)
		}

		// Make a buffered channel to receive event updates on.
		evCh := make(chan conntrack2.Event, *contrackEventBufSize)

		// Listen for all Conntrack and Conntrack-Expect events with 4 decoder goroutines.
		// All errors caught in the decoders are passed on channel errCh.
		errCh, err := c.Listen(evCh, uint8(*contrackEventWorker), []netfilter.NetlinkGroup{netfilter.GroupCTNew, netfilter.GroupCTDestroy})
		if err != nil {
			log.Fatal(err)
		}
		// Start a goroutine to print all incoming messages on the event channel.
		go func() {
			for {
				select {
				case e := <-evCh:
					switch e.Type {
					case conntrack2.EventNew:
						src := e.Flow.TupleOrig.IP.SourceAddress.String()
						des := e.Flow.TupleOrig.IP.DestinationAddress.String()
						protocal := protoLookup(e.Flow.TupleOrig.Proto.Protocol)
						nodeNfConntrackList.WithLabelValues(o.conntrackListLabels(src, des, protocal)...).Inc()
					case conntrack2.EventDestroy:
						src := e.Flow.TupleOrig.IP.SourceAddress.String()
						des := e.Flow.TupleOrig.IP.DestinationAddress.String()
						protocal := protoLookup(e.Flow.TupleOrig.Proto.Protocol)
						nodeNfConntrackList.WithLabelValues(o.conntrackListLabels(src, des, protocal)...).Dec()
					default:
						fmt.Printf("I don't know about type %v", e.Type)
					}
				case err = <-errCh:
					panic(err)
				}
			}
		}()
	*/
}

// protoLookup translates a protocol integer into its string representation.
func protoLookup(p uint8) string {
	protos := map[uint8]string{
		1:   "icmp",
		2:   "igmp",
		6:   "tcp",
		17:  "udp",
		33:  "dccp",
		47:  "gre",
		58:  "ipv6-icmp",
		94:  "ipip",
		115: "l2tp",
		132: "sctp",
		136: "udplite",
	}

	if val, ok := protos[p]; ok {
		return val
	}

	return strconv.FormatUint(uint64(p), 10)
}
