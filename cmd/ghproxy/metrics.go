package main

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	githubAPIRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_ghproxy_github_api_requests_total",
			Help: "Total number of GitHub API requests proxied by ghproxy",
		},
		[]string{"method", "status_code", "resource", "cache"},
	)

	githubAPIUpstreamRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_ghproxy_github_api_upstream_requests_total",
			Help: "Total number of upstream GitHub API requests made by ghproxy",
		},
		[]string{"method", "status_code", "resource", "reason"},
	)
)

func init() {
	prometheus.MustRegister(
		githubAPIRequestsTotal,
		githubAPIUpstreamRequestsTotal,
	)
}
