// Copyright (c) 2021 Ambassador Labs, Inc. See LICENSE for license information.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"

	"github.com/datawire/ambassador/v2/pkg/kates"
	"github.com/datawire/dlib/dlog"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"gopkg.in/yaml.v3"

	_ "embed"
)

//////////////// Variables
// These are really all for Cobra.

var (
	logLevel          logLevelFlag = logLevelInfo
	workloadName      string
	workloadNamespace string
	ingressHost       string
	ingressPort       int
	ingressTLS        bool
	pullRequestURL    string
)

//////////////// Logging/context glue

func ContextWithLogrusLogging(ctx context.Context, cmdname string, loglevel string) context.Context {
	// Grab a Logrus logger and default it to INFO...
	logrusLogger := logrus.New()
	logrusLogger.SetLevel(logrus.InfoLevel)

	// ...then set up dlog and a context with it.
	logger := dlog.WrapLogrus(logrusLogger).
		WithField("PID", os.Getpid()).
		WithField("CMD", cmdname)

	newContext := dlog.WithLogger(ctx, logger)

	// Once _that's_ done, we can check to see if our caller wants a different
	// log level.
	//
	// XXX Why not do this before creating the logger?? Well, we kinda need a
	// context and dlog so that we can log the error if the loglevel is bad...
	if loglevel != "" {
		parsed, err := logrus.ParseLevel(loglevel)

		if err != nil {
			dlog.Errorf(newContext, "Error parsing log level %s: %v", loglevel, err)
		} else {
			logrusLogger.SetLevel(parsed)
		}
	}

	return newContext
}

//////////////// Cobra type stuff

type logLevelFlag string

const (
	logLevelDebug logLevelFlag = "debug"
	logLevelInfo  logLevelFlag = "info"
	logLevelWarn  logLevelFlag = "warn"
	logLevelError logLevelFlag = "error"
)

// String is used both by fmt.Print and by Cobra in help text
func (lvl *logLevelFlag) String() string {
	return string(*lvl)
}

// Set must have pointer receiver so it doesn't change the value of a copy
func (lvl *logLevelFlag) Set(v string) error {
	switch v {
	case "debug", "info", "warn", "error":
		*lvl = logLevelFlag(v)
		return nil

	case "warning":
		*lvl = logLevelWarn
		return nil

	default:
		return errors.New(`must be one of "debug", "info", "warn", "warning", or "error"`)
	}
}

// Type is only used in help text
func (lvl *logLevelFlag) Type() string {
	return "logLevelFlag"
}

//////////////// Inject

//go:embed todd.yaml
var toddYAML []byte

func inject(ctx context.Context, todd map[string]interface{}, un *kates.Unstructured) error {
	name := un.GetName()
	ns := un.GetNamespace()

	if ns == "" {
		ns = "default"
	}

	dlog.Infof(ctx, "Injecting into %s/%s", ns, name)

	spec := un.Object["spec"].(map[string]interface{})

	if spec == nil {
		return errors.New("no spec in object")
	}

	template := spec["template"].(map[string]interface{})

	if template == nil {
		return errors.New("no template in spec")
	}

	tspec := template["spec"].(map[string]interface{})

	if tspec == nil {
		return errors.New("no spec in template")
	}

	containers := tspec["containers"].([]interface{})

	if containers == nil {
		return errors.New("no containers in template")
	}

	// If there's already a container named "todd", we're done.
	for _, c := range containers {
		c := c.(map[string]interface{})
		if c["name"].(string) == "todd" {
			return nil
		}
	}

	tspec["containers"] = append(containers, todd)

	tspec["serviceAccount"] = "ambassador-deploy-previews"
	tspec["serviceAccountName"] = "ambassador-deploy-previews"

	return nil
}

func loadTodd(ctx context.Context) (map[string]interface{}, error) {
	// Expand our Todd template.
	var expanded bytes.Buffer

	t, err := template.New("todd").Parse(string(toddYAML))

	if err != nil {
		return nil, fmt.Errorf("couldn't parse Todd template: %v", err)
	}

	err = t.Execute(&expanded, map[string]interface{}{
		"workloadName":      workloadName,
		"workloadNamespace": workloadNamespace,
		"ingressHost":       ingressHost,
		"ingressPort":       ingressPort,
		"ingressTLS":        ingressTLS,
		"pullRequestURL":    pullRequestURL,
	})

	if err != nil {
		return nil, fmt.Errorf("couldn't expand Todd template: %v", err)
	}

	dlog.Infof(ctx, "expanded Todd template:\n%s", expanded)

	var todd map[string]interface{}
	err = yaml.NewDecoder(bytes.NewReader(expanded.Bytes())).Decode(&todd)

	if err != nil {
		return nil, fmt.Errorf("couldn't parse Todd YAML: %v", err)
	}

	return todd, nil
}

//////////////// Mainline

func cobraMain(cmd *cobra.Command, args []string) {
	// Start by setting up logging, which is intimately tied into setting up
	// our context.
	ctx := ContextWithLogrusLogging(context.Background(), "tinj", string(logLevel))

	errors := 0

	if workloadName == "" {
		dlog.Errorf(ctx, "workloadName must be set")
		errors++
	}

	if ingressHost == "" {
		dlog.Errorf(ctx, "ingressHost must be set")
		errors++
	}

	if workloadNamespace == "" {
		dlog.Errorf(ctx, "workloadNamespace must be set")
		errors++
	}

	if pullRequestURL == "" {
		dlog.Errorf(ctx, "pullRequestURL must be set")
		errors++
	}

	if errors > 0 {
		dlog.Errorf(ctx, "Some required parameters are missing")
		os.Exit(1)
	}

	todd, err := loadTodd(ctx)

	if err != nil {
		dlog.Errorf(ctx, "Error loading Todd: %v", err)
		os.Exit(1)
	}

	decoder := yaml.NewDecoder(os.Stdin)

	for {
		var item interface{}

		err := decoder.Decode(&item)

		if err != nil {
			// Break when there are no more documents to decode
			if err != io.EOF {
				dlog.Errorf(ctx, "Error unmarshaling YAML: %v", err)
				os.Exit(1)
			}
			break
		}

		b, err := json.Marshal(item)

		if err != nil {
			dlog.Errorf(ctx, "Error marshaling JSON: %v", err)
			os.Exit(1)
		}

		// dlog.Infof(ctx, "Item: %#v", item)

		var un kates.Unstructured
		err = json.Unmarshal(b, &un)

		if err != nil {
			dlog.Errorf(ctx, "Error unmarshaling JSON: %v", err)
			os.Exit(1)
		}

		// dlog.Infof(ctx, "Un: %#v", un)

		version := un.GroupVersionKind().GroupVersion().String()
		kind := un.GroupVersionKind().Kind
		name := un.GetName()
		ns := un.GetNamespace()

		if ns == "" {
			ns = "default"
		}

		dlog.Infof(ctx, "%s/%s -- %s, %s", version, kind, name, ns)

		if kind == "Deployment" {
			inject(ctx, todd, &un)
		}

		b, err = yaml.Marshal(un.Object)

		if err != nil {
			dlog.Errorf(ctx, "Error marshaling YAML: %v", err)
			os.Exit(1)
		}

		fmt.Println("---")
		os.Stdout.Write(b)
	}
}

func main() {
	argparser := &cobra.Command{
		Use:   "tinj",
		Short: "Inject Todd into K8s deployment YAML",
		Long: `Inject Todd into K8s deployment YAML.

By default, this command is a filter: feed it YAML that contains a
Kubernetes Deployment, and it will inject a Todd container into it.
It's OK if there are multiple documents in the file; only the first
Deployment will be modified.`,
		Run: cobraMain,
	}
	argparser.PersistentFlags().VarP(&logLevel, "loglevel", "l", "Log level (debug, info, warn, error)")
	argparser.PersistentFlags().StringVarP(&workloadName, "name", "w", "workload", "Name of the workload")
	argparser.PersistentFlags().StringVarP(&workloadNamespace, "namespace", "n", "default", "Namespace of the workload")
	argparser.PersistentFlags().StringVarP(&ingressHost, "ingress-host", "i", "", "Hostname of the ingress")
	argparser.PersistentFlags().IntVarP(&ingressPort, "ingress-port", "p", 443, "Port of the ingress")
	argparser.PersistentFlags().BoolVarP(&ingressTLS, "ingress-tls", "t", true, "Whether the ingress is TLS")
	argparser.PersistentFlags().StringVarP(&pullRequestURL, "pull-request-url", "u", "", "URL of the pull request")

	err := argparser.Execute()

	if err != nil {
		dlog.Errorf(context.Background(), "Error: %v", err)
		os.Exit(1)
	}

	os.Exit(0)
}
