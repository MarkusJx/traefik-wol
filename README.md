# traefik-wol

Traefik plugin which starts a machine when a request is received using wake-on-lan.
Based somewhat on [`go-wol`](https://github.com/sabhiram/go-wol).

## Operation modes
### Using the built-in wake-on-lan service

In this mode, the plugin will try to start the machine using the wake-on-lan service provided by the plugin.
For this to work, the plugin needs to be able to send a magic packet to the machine.
This means that the machine needs to be on the same network as the plugin.
In other words, when using docker the network mode needs to be `host`.

Dynamic configuration example:
```yaml
http:
  middlewares:
    traefik-wol:
      plugin:
        wol:
          healthCheck: http://192.168.0.10:8009 # REQUIRED The URL to use for the health check
          macAddress: 00:00:00:00:00:00 # REQUIRED The MAC address of the machine to start
          ipAddress: 192.168.0.10 # REQUIRED The IP address of the machine to start
```

### Using an external wake-on-lan service
In order to not have to run the plugin in `host` mode, you can use an external wake-on-lan service.
This service needs to be able to send a magic packet to the machine.
The plugin will then send a request to the service to start the machine.

A service that can be used for this is [`docker-wol_api`](https://github.com/rix1337/docker-wol_api).

Dynamic configuration example:
```yaml
http:
  middlewares:
    traefik-wol:
      plugin:
        wol:
          healthCheck: http://192.168.0.10:8009 # REQUIRED The URL to use for the health check
          #REQUIRED The URL to use for the start request
          # This is the URL of the "wake" service
          startUrl: http://192.168.0.11:8081/wol/00:00:00:00:00:00
          startMethod: POST # The method to use for the start request
          numRetries: 5 # The number of retries
          requestTimeout: 5 # The request timeout in seconds
```

## Automatically stopping the machine after a period of inactivity
The plugin can automatically stop the machine after a period of inactivity.
This is done by sending a request to the machine to stop itself.

A service that can be used for this is [`sleep-on-lan`](https://github.com/SR-G/sleep-on-lan).

Dynamic configuration example:
```yaml
http:
  middlewares:
    traefik-wol:
      plugin:
        wol:
          healthCheck: http://192.168.0.10:8009 # REQUIRED The URL to use for the health check
          # REQUIRED The URL to use for the start request
          # This is the URL of the "wake" service
          startUrl: http://192.168.0.11:8081/wol/00:00:00:00:00:00
          startMethod: POST # The method to use for the start request
          numRetries: 5 # The number of retries
          requestTimeout: 5 # The request timeout in seconds
          stopTimeout: 5 # The number of minutes to wait before stopping the server
          stopUrl: http://192.168.0.10:8009/sleep # The URL to use for the stop request
```