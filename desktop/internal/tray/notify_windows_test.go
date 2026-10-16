package tray

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestToastXMLIsWellFormedWhateverTheText(t *testing.T) {
	t.Parallel()

	doc := toastXML(`Sync "failed" <now>`, "a & b",
		[2]string{uploadLabel, actionUpload},
		[2]string{downloadLabel, actionDownload},
	)
	dec := xml.NewDecoder(strings.NewReader(doc))
	var actions []string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "action" {
			for _, a := range el.Attr {
				if a.Name.Local == "arguments" {
					actions = append(actions, a.Value)
				}
			}
		}
	}
	if err := xml.Unmarshal([]byte(doc), new(struct{})); err != nil {
		t.Fatalf("toastXML is not well-formed: %v\n%s", err, doc)
	}
	if len(actions) != 2 || actions[0] != actionUpload || actions[1] != actionDownload {
		t.Errorf("toast actions = %v, want [%s %s]", actions, actionUpload, actionDownload)
	}
	if !strings.Contains(doc, `<audio silent="true"/>`) {
		t.Error("the toast is not silent")
	}
}
