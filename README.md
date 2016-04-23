
send docker container cpu and memory usage to statsd

```
docker run -d -e MARATHON_APP_ID=app1 --name nginx -P nginx

export STATSD_ADDR=127.0.0.1:8125
export STATSD_PREFIX=marathon
```
