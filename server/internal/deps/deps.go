//go:build deps_pin

package deps

// This file ensures all dependencies needed by later phases stay in go.mod
// even before any production code imports them. It is build-tagged off
// (only built with `-tags=deps_pin`) so it has zero runtime cost.

import (
	_ "github.com/gavv/httpexpect/v2"
	_ "github.com/gin-gonic/gin"
	_ "github.com/stretchr/testify/assert"
	_ "github.com/stretchr/testify/mock"
	_ "github.com/stretchr/testify/require"
	_ "github.com/testcontainers/testcontainers-go"
	_ "github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "github.com/testcontainers/testcontainers-go/modules/redis"
	_ "go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	_ "go.opentelemetry.io/contrib/instrumentation/runtime"
	_ "go.opentelemetry.io/otel"
	_ "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	_ "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	_ "go.opentelemetry.io/otel/sdk"
	_ "go.opentelemetry.io/otel/sdk/metric"
	_ "gorm.io/driver/postgres"
	_ "gorm.io/gorm"
	_ "gorm.io/plugin/opentelemetry/tracing"
)
