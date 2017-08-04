package collector

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"regexp"
	"strconv"
	"io/ioutil"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
)

type GenericExporter struct {
	logger log.Logger
	client *http.Client
	url    *url.URL
	URI_path string
	subsystem string

	gauges map[string]prometheus.Gauge

	up                              prometheus.Gauge
	totalScrapes, jsonParseFailures prometheus.Counter
}

func GetSubsystem(URI_path string) string {
	strip_leading_slash := regexp.MustCompile("^/?_?([^/_]+)")
	convert_slash_to_underscore := regexp.MustCompile("/_?([^/])")

	subsystem := strip_leading_slash.ReplaceAllString(URI_path, "${1}")
	subsystem = convert_slash_to_underscore.ReplaceAllString(subsystem, "_${1}")

	return subsystem
}

func NewGenericQuery(logger log.Logger, client *http.Client, url *url.URL, URI_path string) *GenericExporter {
	subsystem := GetSubsystem(URI_path)
	gauges := make(map[string]prometheus.Gauge)

	exporter := GenericExporter{
		logger: logger,
		client: client,
		url:    url,
		URI_path: URI_path,
		subsystem: subsystem,

		gauges: gauges,

		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: prometheus.BuildFQName(namespace, subsystem, "up"),
			Help: "Was the last scrape of the ElasticSearch cluster health endpoint successful.",
		}),
		totalScrapes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: prometheus.BuildFQName(namespace, subsystem, "total_scrapes"),
			Help: "Current total ElasticSearch cluster health scrapes.",
		}),
		jsonParseFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: prometheus.BuildFQName(namespace, subsystem, "json_parse_failures"),
			Help: "Number of errors while parsing JSON.",
		}),
	}

	return &exporter
}

func (c *GenericExporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.up.Desc()
	ch <- c.totalScrapes.Desc()
	ch <- c.jsonParseFailures.Desc()

	for _, g := range c.gauges {
		g.Describe(ch)
	}
}

func (c *GenericExporter) Collect(ch chan<- prometheus.Metric) {
	full_path := *c.url
	full_path.Path = c.URI_path
	c.totalScrapes.Inc()
	defer func() {
		ch <- c.up
		ch <- c.totalScrapes
		ch <- c.jsonParseFailures
	}()

	resp, err := c.client.Get(full_path.String())
	if err != nil {
		c.up.Set(0)
		level.Warn(c.logger).Log(
			"msg", "Error while querying Json endpoint.",
			"err", err,
		)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		level.Warn(c.logger).Log(
			"msg", "Failed to read Json response body.",
			"err", err,
		)
		c.up.Set(0)
	}
	resp.Body.Close()

	c.up.Set(1)

	var allStats map[string]interface{}
	err = json.Unmarshal(body, &allStats)
	if err != nil {
		level.Warn(c.logger).Log(
			"msg", "Failed to unmarshal JSON into struct.",
			"err", err,
		)
	}

	// Extracrt the metrics from the json interface
	c.extractJSON("", allStats)

	// Report metrics
	for _, g := range c.gauges {
		g.Collect(ch)
	}
}

func (c *GenericExporter) addGauge(name string, subsystem string, value float64, help string) {
	name = strings.ToLower(name)
	c.gauges[name] = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: namespace, Subsystem: subsystem, Name: name, Help: help})
	c.gauges[name].Set(value)
}

func (c *GenericExporter) extractJSON(metric string, jsonInt map[string]interface{}) {
	newMetric := ""
	debug := false
	fix_double_underscore := regexp.MustCompile("^_(.+)")

	for k, v := range jsonInt {
		if len(metric) > 0 {
			newMetric = metric + "_" + k
			newMetric = fix_double_underscore.ReplaceAllString(newMetric, "$1")
		} else {
			newMetric = k
		}
		switch vv := v.(type) {
		case string:
			if debug {
				level.Warn(c.logger).Log(
					newMetric, "is string",
					"type", vv,
				)
			}
			//Handle the case where the string contains json value
			if len(vv) > 2 && vv[0] == '{' {
				var stats map[string]interface{}
				err := json.Unmarshal([]byte(vv), &stats)
				if err != nil {
					level.Warn(c.logger).Log(
						"Failed to parse json from string", newMetric,
						"err", err,
					)
				} else {
					if debug {
						level.Warn(c.logger).Log(
							"Extracting json values from the string ", newMetric,
						)
					}
					c.extractJSON(newMetric, stats)
				}
			}
		case int:
			if debug {
				level.Warn(c.logger).Log(
					newMetric, "is int",
					"type", vv,
				)
			}
			c.addGauge(newMetric, c.subsystem, float64(vv), newMetric)
		case float64:
			if debug {
				level.Warn(c.logger).Log(
					newMetric, "is float",
					"type", vv,
				)
			}
			c.addGauge(newMetric, c.subsystem, vv, newMetric)
		case bool:
			if vv {
				if debug {
					level.Warn(c.logger).Log(
						newMetric, "is a bool => 1",
					)
				}
				c.addGauge(newMetric, c.subsystem, float64(1), newMetric)
			} else {
				if debug {
					level.Warn(c.logger).Log(
						newMetric, "is a bool => 0",
					)
				}
				c.addGauge(newMetric, c.subsystem, float64(0), newMetric)
			}
		case map[string]interface{}:
			if debug {
				level.Warn(c.logger).Log(
					newMetric, "is a hash",
				)
			}
			c.extractJSON(newMetric, vv)
		case []interface{}:
			if debug {
				level.Warn(c.logger).Log(
					newMetric, "is an array",
				)
			}
			c.extractJSONArray(newMetric, vv)
		default:
			if debug {
				level.Warn(c.logger).Log(
					newMetric, "is of a type I don't know how to handle",
				)
			}
		}
	}
}

// Extract metrics from json array interface
func (c *GenericExporter) extractJSONArray(metric string, jsonInt []interface{}) {
	newMetric := ""
	debug := false
	for k, v := range jsonInt {
		if len(metric) > 0 {
			newMetric = metric + "_" + strconv.Itoa(k)
		} else {
			newMetric = strconv.Itoa(k)
		}
		switch vv := v.(type) {
		case string:
			if debug {
				level.Warn(c.logger).Log(
					newMetric, "is string",
					"type", vv,
				)
			}
			if len(vv) > 2 && vv[0] == '{' {
				var stats map[string]interface{}
				err := json.Unmarshal([]byte(vv), &stats)
				if err != nil {
					level.Warn(c.logger).Log(
						"Failed to parse json from string", newMetric,
						"err", err,
					)
				} else {
					c.extractJSON(newMetric, stats)
					if debug {
						level.Warn(c.logger).Log(
							"Extracting json values from the string ", newMetric,
						)
					}
				}
			}
		case int:
			if debug {
				level.Warn(c.logger).Log(
					newMetric, "is int",
					"type", vv,
				)
			}
			c.addGauge(newMetric, c.subsystem, float64(vv), newMetric)
		case float64:
			if debug {
				level.Warn(c.logger).Log(
					newMetric, "is float",
					"type", vv,
				)
			}
			c.addGauge(newMetric, c.subsystem, vv, newMetric)
		case bool:
			if vv {
				if debug {
					level.Warn(c.logger).Log(
						newMetric, "is bool => 1",
					)
				}
				c.addGauge(newMetric, c.subsystem, float64(1), newMetric)
			} else {
				if debug {
					level.Warn(c.logger).Log(
						newMetric, "is bool => 0",
					)
				}
				c.addGauge(newMetric, c.subsystem, float64(0), newMetric)
			}
		case map[string]interface{}:
			if debug {
				level.Warn(c.logger).Log(
					newMetric, "is hash",
				)
			}
			c.extractJSON(newMetric, vv)
		case []interface{}:
			if debug {
				level.Warn(c.logger).Log(
					newMetric, "is an array",
				)
			}
			c.extractJSONArray(newMetric, vv)
		default:
			if debug {
				level.Warn(c.logger).Log(
					newMetric, "is of a type I don't know how to handle",
				)
			}
		}
	}
}
