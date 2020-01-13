module git.qutoutiao.net/conntrack-exporter

go 1.12

replace github.com/cmattoon/conntrackr => github.com/zjj2wry/conntrackr v0.1.4

require (
	github.com/cmattoon/conntrackr v0.0.0-20190507024333-e908420c06e3
	github.com/golang/glog v0.0.0-20160126235308-23def4e6c14b
	github.com/imdario/mergo v0.3.8 // indirect
	github.com/prometheus/client_golang v1.3.0
	golang.org/x/oauth2 v0.0.0-20200107190931-bf48bf16ab8d // indirect
	golang.org/x/time v0.0.0-20191024005414-555d28b269f0 // indirect
	k8s.io/api v0.17.0
	k8s.io/apimachinery v0.17.0
	k8s.io/client-go v0.17.0
	k8s.io/utils v0.0.0-20200109141947-94aeca20bf09 // indirect
)
