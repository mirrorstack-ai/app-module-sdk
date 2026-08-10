package registry

import "testing"

func TestUIPageLabelMapsAreDeepCopied(t *testing.T) {
	t.Parallel()

	titleLabels := map[string]string{"zh-TW": "影片"}
	descriptionLabels := map[string]string{"zh-TW": "影片說明"}
	r := New()
	r.SetUI(ModuleUI{DefaultPages: []UIPage{{
		Route:             "/",
		Title:             "Videos",
		TitleLabels:       titleLabels,
		Description:       "Video description",
		DescriptionLabels: descriptionLabels,
		Export:            "VideosPage",
	}}})

	titleLabels["zh-TW"] = "mutated caller title"
	descriptionLabels["zh-TW"] = "mutated caller description"
	got := r.UI()
	if got.DefaultPages[0].TitleLabels["zh-TW"] != "影片" {
		t.Errorf("registry TitleLabels mutated through caller map: %v", got.DefaultPages[0].TitleLabels)
	}
	if got.DefaultPages[0].DescriptionLabels["zh-TW"] != "影片說明" {
		t.Errorf("registry DescriptionLabels mutated through caller map: %v", got.DefaultPages[0].DescriptionLabels)
	}

	got.DefaultPages[0].TitleLabels["zh-TW"] = "mutated return title"
	got.DefaultPages[0].DescriptionLabels["zh-TW"] = "mutated return description"
	again := r.UI()
	if again.DefaultPages[0].TitleLabels["zh-TW"] != "影片" {
		t.Errorf("registry TitleLabels mutated through UI return: %v", again.DefaultPages[0].TitleLabels)
	}
	if again.DefaultPages[0].DescriptionLabels["zh-TW"] != "影片說明" {
		t.Errorf("registry DescriptionLabels mutated through UI return: %v", again.DefaultPages[0].DescriptionLabels)
	}
}
