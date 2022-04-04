# TINJ

This is the Todd Injector.

Usage: `go run github.com/datawire/tinj [options]`

It's a filter. Feed YAML containing a Deployment into it. Things that aren't Deployments
will be ignored. Currently all Deployments are edited.

```yaml
cat deployment.yaml | \
   go run github.com/datawire/tinj \
      -w ffs -n ffsns \
      --ingress-host klf.example.com \
      -u https://ffs.example.com/pr/999 | \
   kubectl apply -f -
```
