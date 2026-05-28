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
	case nil:
		*d = 0
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

func (d Duration) MarshalYAML() (any, error) {
	if d == 0 {
		return nil, nil
	}
	return time.Duration(d).String(), nil
}

type Duration time.Duration
