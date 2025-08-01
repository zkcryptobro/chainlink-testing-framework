package framework

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/go-playground/locales/en"
	ut "github.com/go-playground/universal-translator"
	"github.com/go-playground/validator/v10"
	"github.com/pelletier/go-toml/v2"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const (
	DefaultConfigDir = "."
)

const (
	EnvVarTestConfigs = "CTF_CONFIGS"
	//nolint
	EnvVarAWSSecretsManager = "CTF_AWS_SECRETS_MANAGER"
	// EnvVarCI this is a default env variable many CI runners use so code can detect we run in CI
	EnvVarCI = "CI"
)

const (
	DefaultConfigFilePath    = "env.toml"
	DefaultOverridesFilePath = "overrides.toml"
)

var (
	DefaultNetworkName  = "ctf"
	Validator           = validator.New(validator.WithRequiredStructEnabled())
	ValidatorTranslator ut.Translator
)

func init() {
	eng := en.New()
	uni := ut.New(eng, eng)
	ValidatorTranslator, _ = uni.GetTranslator("en")
}

type ValidationError struct {
	Field   string
	Value   interface{}
	Message string
}

// mergeInputs merges all EnvVarTestConfigs filenames into one files, starting from the last and applying to the first
func mergeInputs[T any]() (*T, error) {
	var config T
	paths := strings.Split(os.Getenv(EnvVarTestConfigs), ",")
	_, err := BaseConfigPath()
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		L.Info().Str("Path", path).Msg("Loading configuration input")
		data, err := os.ReadFile(filepath.Join(DefaultConfigDir, path))
		if err != nil {
			if path == DefaultOverridesFilePath {
				L.Info().Str("Path", path).Msg("Overrides file not found or empty")
				continue
			}
			return nil, fmt.Errorf("error reading config file %s: %w", path, err)
		}
		if L.GetLevel() == zerolog.TraceLevel {
			fmt.Println(string(data))
		}

		data, err = transformAllOverrideModeForNodeSets(data)
		if err != nil {
			return nil, fmt.Errorf("error transforming node specs: %w", err)
		}

		decoder := toml.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&config); err != nil {
			var details *toml.StrictMissingError
			if errors.As(err, &details) {
				fmt.Println(details.String())
			}
			return nil, fmt.Errorf("failed to decode TOML config, strict mode: %s", err)
		}
	}
	if L.GetLevel() == zerolog.TraceLevel {
		L.Trace().Msg("Merged inputs")
		spew.Dump(config)
	}
	return &config, nil
}

func validateWithCustomErr(cfg interface{}) []ValidationError {
	var validationErrors []ValidationError
	err := Validator.Struct(cfg)
	if err != nil {
		//nolint
		for _, err := range err.(validator.ValidationErrors) {
			customMessage := err.Translate(ValidatorTranslator)
			defaultMessage := fmt.Sprintf("validation failed on '%s' with tag '%s'", err.Field(), err.Tag())

			messageToUse := customMessage
			if strings.HasPrefix(customMessage, "validation failed") {
				messageToUse = defaultMessage
			}

			validationErrors = append(validationErrors, ValidationError{
				Field:   err.StructNamespace(),
				Value:   err.Value(),
				Message: messageToUse,
			})
		}
	}
	return validationErrors
}

func validate(s interface{}) error {
	errs := validateWithCustomErr(s)
	for _, e := range errs {
		L.Error().Any("error", e).Send()
	}
	if len(errs) > 0 {
		return fmt.Errorf("config validation failed\nwe are using 'go-playground/validator', please read more here: https://github.com/go-playground/validator?tab=readme-ov-file#usage-and-documentation")
	}
	return nil
}

// transformAllOverrideModeForNodeSets we need this function so the test logic can be the same in both "each" and "all" override modes
// we can't do UnmarshalTOML or UnmarshalText because our TOML library do not support it
func transformAllOverrideModeForNodeSets(data []byte) ([]byte, error) {
	var config map[string]interface{}
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	nodesets, ok := config["nodesets"].([]interface{})
	if !ok {
		return data, nil
	}
	for _, nodesetInterface := range nodesets {
		nodeset, ok := nodesetInterface.(map[string]interface{})
		if !ok {
			continue
		}
		if nodeset["override_mode"] != "all" {
			continue
		}
		nodes, ok := nodeset["nodes"].(int64)
		if !ok || nodes <= 0 {
			return nil, fmt.Errorf("nodesets.nodes must be provided")
		}
		specs, ok := nodeset["node_specs"].([]interface{})
		if !ok || len(specs) == 0 {
			return nil, fmt.Errorf("nodesets.node_specs must be provided")
		}
		firstSpec := specs[0].(map[string]interface{})
		expanded := make([]interface{}, nodes)
		for i := range expanded {
			newSpec := make(map[string]interface{})
			for k, v := range firstSpec {
				newSpec[k] = v
			}
			expanded[i] = newSpec
		}
		nodeset["node_specs"] = expanded
	}
	return toml.Marshal(config)
}

func Load[X any](t *testing.T) (*X, error) {
	input, err := mergeInputs[X]()
	if err != nil {
		return input, err
	}
	if err := validate(input); err != nil {
		return nil, err
	}
	if t != nil {
		t.Cleanup(func() {
			err := Store[X](input)
			require.NoError(t, err)
		})
	}
	if err = DefaultNetwork(nil); err != nil {
		L.Info().Err(err).Msg("docker network creation failed, either docker is not running or you are running in CRIB mode")
	}
	return input, nil
}

// LoadCache loads cached config with environment values
func LoadCache[X any](t *testing.T) (*X, error) {
	cfgPath := os.Getenv("CTF_CONFIGS")
	L.Debug().Str("CTFConfigs", cfgPath).Msg("Loading configuration from cache")
	if cfgPath == "" {
		return nil, fmt.Errorf("CTF_CONFIGS environment variable not set when loading from cache")
	}
	firstConfig := strings.Split(cfgPath, ",")
	cfgParts := strings.Split(firstConfig[0], ".")
	if len(cfgParts) != 2 {
		return nil, fmt.Errorf("invalid config path when loading from cache: %s", cfgPath)
	}
	_ = os.Setenv("CTF_CONFIGS", fmt.Sprintf("%s-cache.%s", cfgParts[0], cfgParts[1]))
	return Load[X](t)
}

// BaseConfigPath returns base config path, ex. env.toml,overrides.toml -> env.toml
func BaseConfigPath() (string, error) {
	configs := os.Getenv("CTF_CONFIGS")
	if configs == "" {
		return "", fmt.Errorf("no %s env var is provided, you should provide at least one test config in TOML", EnvVarTestConfigs)
	}
	L.Debug().Str("DevEnvConfigs", configs).Msg("Getting base config path")
	return strings.Split(configs, ",")[0], nil
}

// BaseConfigName returns base config name, ex. env.toml -> env
func BaseConfigName() (string, error) {
	cp, err := BaseConfigPath()
	if err != nil {
		return "", err
	}
	return strings.Replace(cp, ".toml", "", -1), nil
}

// BaseCacheName returns base cache file name, ex.: env.toml -> env-cache.toml
func BaseCacheName() (string, error) {
	cp, err := BaseConfigPath()
	if err != nil {
		return "", err
	}
	name := strings.Replace(cp, ".toml", "", -1)
	return fmt.Sprintf("%s-cache.toml", name), nil
}

func DefaultNetwork(_ *sync.Once) error {
	netCmd := exec.Command("docker", "network", "create", DefaultNetworkName)
	out, err := netCmd.CombinedOutput()
	L.Debug().Str("Out", string(out)).Msg("Creating Docker network")
	if err != nil {
		if strings.Contains(string(out), "already exists") {
			return nil
		}
		return err
	}
	return err
}

func RenderTemplate(tmpl string, data interface{}) (string, error) {
	var buf bytes.Buffer
	err := template.Must(template.New("tmpl").Parse(tmpl)).Execute(&buf, data)
	return buf.String(), err
}

func getBaseConfigPath() (string, error) {
	configs := os.Getenv("CTF_CONFIGS")
	if configs == "" {
		return "", fmt.Errorf("no %s env var is provided, you should provide at least one test config in TOML", EnvVarTestConfigs)
	}
	return strings.Split(configs, ",")[0], nil
}

func Store[T any](cfg *T) error {
	baseConfigPath, err := getBaseConfigPath()
	if err != nil {
		return err
	}
	newCacheName := strings.Replace(baseConfigPath, ".toml", "", -1)
	if strings.Contains(newCacheName, "cache") {
		L.Info().Str("Cache", baseConfigPath).Msg("Cache file already exists, skipping")
		return nil
	}
	cachedOutName := fmt.Sprintf("%s-cache.toml", strings.Replace(baseConfigPath, ".toml", "", -1))
	L.Info().Str("OutputFile", cachedOutName).Msg("Storing configuration output")
	d, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(DefaultConfigDir, cachedOutName), d, 0600)
}

// JSONStrDuration is JSON friendly duration that can be parsed from "1h2m0s" Go format
type JSONStrDuration struct {
	time.Duration
}

func (d *JSONStrDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

func (d *JSONStrDuration) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case string:
		var err error
		d.Duration, err = time.ParseDuration(value)
		if err != nil {
			return err
		}
		return nil
	default:
		return errors.New("invalid duration")
	}
}

// MustParseDuration parses a duration string in Go's format and returns the corresponding time.Duration.
// It panics if the string cannot be parsed, ensuring that the caller receives a valid duration.
func MustParseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		L.Fatal().Msg("cannot parse duration, should be Go format 1h2m3s")
	}
	return d
}
