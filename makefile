REGISTRY := baizhi

build:
	GOOS=linux GOARCH=amd64 go build -v -o conntrack-exporter main.go

docker:
	docker build -t $(REGISTRY)/conntrack:latest . -f Dockerfile
	docker push $(REGISTRY)/conntrack:latest

deploy:
	kubectl apply -f deploy.yaml -n monitoring
	kubectl get po -n monitoring -l k8s-app=conntrack-exporter -o wide -w

prune:
	kubectl delete -f deploy.yaml -n monitoring
