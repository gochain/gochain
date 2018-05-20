package netstats

import "testing"

func TestParseConfig(t *testing.T) {
	for _, test := range []parseConfigTest{
		{
			flag: "name:secret@host:port",
			exp: Config{
				Name:   "name",
				Secret: "secret",
				URL:    "host:port",
			},
		},
		{
			flag: "Pretty Name:secret@host:port",
			exp: Config{
				Name:   "Pretty Name",
				Secret: "secret",
				URL:    "host:port",
			},
		},
		{
			flag: ":secret@host:port",
			exp: Config{
				Secret: "secret",
				URL:    "host:port",
			},
		},
		{
			flag: "@host:port",
			exp: Config{
				URL: "host:port",
			},
		},
	} {
		t.Run(test.flag, test.run)
	}
}

type parseConfigTest struct {
	flag string
	exp  Config
}

func (test *parseConfigTest) run(t *testing.T) {
	got, err := ParseConfig(test.flag)
	if err != nil {
		t.Error(err)
	} else if got != test.exp {
		t.Errorf("expected %#v but got %#v", test.exp, got)
	}
}
