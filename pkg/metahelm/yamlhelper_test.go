package metahelm

import (
	"testing"

	yaml "gopkg.in/yaml.v2"
)

type testOverrides struct {
	Image string `yaml:"image"`
	Data  struct {
		Foo int `yaml:"foo"`
	} `yaml:"data"`
}

func TestYAMLHelperSingleLevel(t *testing.T) {
	d := map[string]string{"image": "asdf"}
	c := Chart{}
	if err := c.ToYAMLStream(d); err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	ds := testOverrides{}
	if err := yaml.Unmarshal(c.ValueOverrides, &ds); err != nil {
		t.Fatalf("error unmarshaling: %v", err)
	}
	if ds.Image != d["image"] {
		t.Fatalf("bad value: %v", ds.Image)
	}
}

func TestYAMLHelperNestedLevel(t *testing.T) {
	d := map[string]string{"data.foo": "1234"}
	c := Chart{}
	if err := c.ToYAMLStream(d); err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	ds := testOverrides{}
	if err := yaml.Unmarshal(c.ValueOverrides, &ds); err != nil {
		t.Fatalf("error unmarshaling: %v", err)
	}
	if ds.Data.Foo != 1234 {
		t.Fatalf("bad value: %v", ds.Data.Foo)
	}
}
