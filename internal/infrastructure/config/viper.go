package config

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// EnvPrefix prefixes every environment variable, e.g. GASOMETER_DATABASE_URL.
const EnvPrefix = "gasometer"

var dashesToUnderscores = strings.NewReplacer("-", "_")

// EnvVarName is the environment variable for key.
func EnvVarName(key string) string {
	return strings.ToUpper(EnvPrefix + "_" + dashesToUnderscores.Replace(key))
}

// BuildViper parses args into fs and layers flags over environment over the config file or content. Deprecation
// warnings go to warn.
func BuildViper(fs *pflag.FlagSet, args []string, warn io.Writer) (*viper.Viper, error) {
	if err := deprecateFlags(fs); err != nil {
		return nil, err
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	v := viper.New()
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(dashesToUnderscores)
	v.SetEnvPrefix(EnvPrefix)
	if err := v.BindPFlags(fs); err != nil {
		return nil, err
	}

	switch {
	case v.IsSet(ConfigContentKey) && v.GetString(ConfigContentKey) != "":
		content, err := base64.StdEncoding.DecodeString(v.GetString(ConfigContentKey))
		if err != nil {
			return nil, fmt.Errorf("decode base64 config content: %w", err)
		}
		v.SetConfigType(v.GetString(ConfigContentTypeKey))
		if err := v.ReadConfig(bytes.NewBuffer(content)); err != nil {
			return nil, fmt.Errorf("read config content: %w", err)
		}
	case v.GetString(ConfigFileKey) != "":
		v.SetConfigFile(os.ExpandEnv(v.GetString(ConfigFileKey)))
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config file: %w", err)
		}
	}

	WarnDeprecated(v, deprecatedKeys, warn)
	return v, nil
}

// WarnDeprecated writes a warning for each deprecated key the config file still sets.
func WarnDeprecated(v *viper.Viper, deprecated map[string]string, warn io.Writer) {
	for key, message := range deprecated {
		if v.InConfig(key) {
			_, _ = fmt.Fprintf(warn, "config %s is deprecated: %s\n", key, message)
		}
	}
}
