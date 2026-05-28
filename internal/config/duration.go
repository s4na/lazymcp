package config

import (
	"fmt"
	"time"
)

func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var raw any
	if err := unmarshal(&raw); err != nil {
		return err
	}
	switch v := raw.(type) {
	case int:
		*d = Duration(time.Duration(v) * time.Second)
	case string:
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return err
		}
		*d = Duration(parsed)
	default:
		return fmt.Errorf("unsupported duration value %T", raw)
	}
	return nil
}

type Duration time.Duration
