package parser_test

import (
	"encoding/json"
	"encoding/xml"

	"gopkg.in/yaml.v3"
)

type Config struct {
	AppName    string   `xml:"app_name" json:"app_name" yaml:"app_name"`
	AppVersion string   `xml:"app_version" json:"app_version" yaml:"app_version"`
	Settings   Settings `xml:"settings" json:"settings" yaml:"settings"`
	Servers    Servers  `xml:"servers" json:"servers" yaml:"servers"`
	Metadata   Metadata `xml:"metadata" json:"metadata" yaml:"metadata"`
}

type Settings struct {
	Debug    bool     `xml:"debug" json:"debug" yaml:"debug"`
	Port     int      `xml:"port" json:"port" yaml:"port"`
	Features Features `xml:"features" json:"features" yaml:"features"`
	Options  []Option `xml:"options>option" json:"options" yaml:"options"`
}

type Features struct {
	Feature []string `xml:"feature" json:"feature" yaml:"feature"`
	Test    string   `xml:"test" json:"test" yaml:"test"`
}

type Option struct {
	Enabled bool   `xml:"enabled,attr" json:"enabled" yaml:"enabled"`
	Name    string `xml:"name" json:"name" yaml:"name"`
	Value   int    `xml:"value" json:"value" yaml:"value"`
}

type Servers struct {
	Server []Server `xml:"server" json:"server" yaml:"server"`
}

type Server struct {
	Name string `xml:"name" json:"name" yaml:"name"`
	IP   string `xml:"ip" json:"ip" yaml:"ip"`
}

type Metadata struct {
	Created string `xml:"created" json:"created" yaml:"created"`
	Updated string `xml:"updated" json:"updated" yaml:"updated"`
	Tags    Tags   `xml:"tags" json:"tags" yaml:"tags"`
}

type Tags struct {
	Tag []string `xml:"tag" json:"tag" yaml:"tag"`
}

var ConfigData = Config{
	AppName:    "TestApp",
	AppVersion: "1.0",
	Settings: Settings{
		Debug: true,
		Port:  8080,
		Features: Features{
			Feature: []string{"login", "signup", "test", "profile"},
			Test:    "test",
		},
		Options: []Option{
			{
				Enabled: true,
				Name:    "option1",
				Value:   123,
			},
			{
				Enabled: false,
				Name:    "option2",
				Value:   456,
			},
		},
	},
	Servers: Servers{
		Server: []Server{
			{
				Name: "server1",
				IP:   "192.168.1.1",
			},
			{
				Name: "server2",
				IP:   "192.168.1.2",
			},
		},
	},
	Metadata: Metadata{
		Created: "2021-01-01",
		Updated: "2021-01-02",
		Tags: Tags{
			Tag: []string{"tag1", "tag2", "tag3"},
		},
	},
}

var (
	XMLContent, XMLErr   = xml.Marshal(ConfigData)
	JSONContent, JSONErr = json.Marshal(ConfigData)
	YAMLContent, YAMLErr = yaml.Marshal(ConfigData)
	ExpectedConfig       = map[string]string{
		"app.name":                    "TestApp",
		"app.version":                 "1.0",
		"settings.debug":              "true",
		"settings.port":               "8080",
		"settings.features.feature.0": "login",
		"settings.features.feature.1": "signup",
		"settings.features.feature.2": "test",
		"settings.features.feature.3": "profile",
		"settings.features.test":      "test",
		"settings.options.0.enabled":  "true",
		"settings.options.0.name":     "option1",
		"settings.options.0.value":    "123",
		"settings.options.1.enabled":  "false",
		"settings.options.1.name":     "option2",
		"settings.options.1.value":    "456",
		"servers.server.0.name":       "server1",
		"servers.server.0.ip":         "192.168.1.1",
		"servers.server.1.name":       "server2",
		"servers.server.1.ip":         "192.168.1.2",
		"metadata.created":            "2021-01-01",
		"metadata.updated":            "2021-01-02",
		"metadata.tags.tag.0":         "tag1",
		"metadata.tags.tag.1":         "tag2",
		"metadata.tags.tag.2":         "tag3",
	}
)
