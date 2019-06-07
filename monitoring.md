## How to configure private cluster monitoring
# netstat
```
docker run -d -p 80:80 --name netstats -e PORT=80 -e WS_SECRET=SELECTED_SECRET
gochain/netstats
```

# net-intelligence-api
1. Create app.json file with following content(one node example):
```
[
  {
    "name"              : "node-app",
    "script"            : "app.js",
    "log_date_format"   : "YYYY-MM-DD HH:mm Z",
    "merge_logs"        : false,
    "watch"             : false,
    "max_restarts"      : 10,
    "exec_interpreter"  : "node",
    "exec_mode"         : "fork_mode",
    "env":
    {
      "NODE_ENV"        : "test",
      "RPC_HOST"        : "RPC_HOST_OF_NODE",
      "RPC_PORT"        : "8545",
      "INSTANCE_NAME"   : "node1",
      "CONTACT_DETAILS" : "",
      "WS_SERVER"       : "WS_SERVER_URL",
      "WS_SECRET"       : "WS_SECRET",
      "VERBOSITY"       : 2
    }
  },
]
```

Where WS_SECRET is a secret which you use to launch netstat and WS_SERVER is a netstats address (ie http://localhost )
and
```
docker run -d --net=host --name net-intelligence -v $PWD/app.json:/home/ethnetintel/eth-net-intelligence-api/app.json  gochain/net-intelligence-api
```
