package config

import "time"

type ServerConfig struct {
	Version          Version           `mapstructure:"version"`
	Log              LogConfig         `mapstructure:"log"`
	Serve            Serve             `mapstructure:"serve"`
	Telemetry        TelemetryConfig   `mapstructure:"telemetry"`
	ResourceManagers []ResourceManager `mapstructure:"resource_managers"`
	Plugin           PluginConfig      `mapstructure:"plugin"`
	Replay           ReplayConfig      `mapstructure:"replay"`
	Publisher        *Publisher        `mapstructure:"publisher"`
}

type Serve struct {
	Port        int      `mapstructure:"port" default:"9100"` // port to listen on
	IngressHost string   `mapstructure:"ingress_host"`        // service ingress host for jobs to communicate back to optimus
	AppKey      string   `mapstructure:"app_key"`             // random 32 character hash used for encrypting secrets
	DB          DBConfig `mapstructure:"db"`
}

type DBConfig struct {
	DSN               string `mapstructure:"dsn"`                              // data source name e.g.: postgres://user:password@host:123/database?sslmode=disable
	MinOpenConnection int    `mapstructure:"min_open_connection" default:"5"`  // minimum open DB connections
	MaxOpenConnection int    `mapstructure:"max_open_connection" default:"20"` // maximum allowed open DB connections
}

type TelemetryConfig struct {
	ProfileAddr string `mapstructure:"profile_addr"`
	JaegerAddr  string `mapstructure:"jaeger_addr"`
}

type ResourceManager struct {
	Name        string      `mapstructure:"name"`
	Type        string      `mapstructure:"type"`
	Description string      `mapstructure:"description"`
	Config      interface{} `mapstructure:"config"`
}

type ResourceManagerConfigOptimus struct {
	Host    string            `mapstructure:"host"`
	Headers map[string]string `mapstructure:"headers"`
}

type PluginConfig struct {
	Artifacts []string `mapstructure:"artifacts"`
}

// TODO: add worker interval
type ReplayConfig struct {
	ReplayTimeout time.Duration `mapstructure:"replay_timeout" default:"3h"`
}

type Publisher struct {
	Type   string      `mapstructure:"type" default:"kafka"`
	Buffer int         `mapstructure:"buffer"`
	Config interface{} `mapstructure:"config"`
}

type PublisherKafkaConfig struct {
	Topic               string   `mapstructure:"topic"`
	BatchIntervalSecond int      `mapstructure:"batch_interval_second"`
	BrokerURLs          []string `mapstructure:"broker_urls"`
}
