FROM centos:7

ADD ./ /data/app

WORKDIR /data/app

ENTRYPOINT ["sh","-c","./conntrack-exporter"]
