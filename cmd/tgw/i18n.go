package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// siteLanguage is a language the site is published in.
type siteLanguage struct {
	Code string `json:"c"`
	Name string `json:"n"` // the language's own name
	Base string `json:"b"` // URL path its pages are served under
}

// languageNames are the published languages' own names, as MediaWiki's
// language selector shows them.
var languageNames = map[string]string{"en": "English", "vi": "Tiếng Việt"}

// uiMessages translate the interface: the chrome, page furniture and the
// generated listings. The keys are the English text the site writes, and
// the values are the wiki's own messages for the language, taken from
// api.php?action=query&meta=allmessages&amlang=vi, so the copy reads as
// the wiki does with ?uselang=vi. The few that are this copy's own are
// marked.
var uiMessages = map[string]map[string]string{
	"vi": {
		// Header and drawer.
		"Toggle search":                     "Tìm kiếm",
		"Search Tolkien Gateway":            "Tìm kiếm trên Tolkien Gateway",
		"Search":                            "Tìm kiếm",
		"Random page":                       "Trang ngẫu nhiên",
		"Select item":                       "Chọn mục",
		"Open search":                       "Mở tìm kiếm",
		"Exit search":                       "Thoát tìm kiếm",
		"Searching titles on this copy":     "Tìm theo tên trang trên bản sao này", // this copy's own
		"Toggle menu":                       "Bật tắt bảng chọn",
		"Visit the main page":               "Đi đến Trang Chính",
		"articles":                          "bài viết",
		"files":                             "tập tin",
		"users":                             "người dùng",
		"edits":                             "sửa đổi",
		"Navigation":                        "Điều hướng",
		"Main Page":                         "Trang Chính",
		"Visit the main page [alt-shift-z]": "Đi đến Trang Chính [alt-shift-z]",
		"Recent changes":                    "Thay đổi gần đây",
		"A list of recent changes in the wiki [alt-shift-r]": "Danh sách thay đổi gần đây trong wiki [alt-shift-r]",
		"Load a random page [alt-shift-x]":                   "Xem trang ngẫu nhiên [alt-shift-x]",
		"Community Portal":                                   "Cộng đồng",
		"Help":                                               "Trợ giúp",
		"Special pages":                                      "Trang đặc biệt",
		"A list of all special pages [alt-shift-q]":          "Một danh sách chứa tất cả trang đặc biệt [alt-shift-q]",
		"Upload file":                                        "Tải lên tập tin",
		"Upload files [alt-shift-u]":                         "Tải tập tin lên [alt-shift-u]",
		"Toggle preferences menu":                            "Tùy chọn giao diện",
		"Notifications":                                      "Thông báo",
		"Toggle personal menu":                               "Bật tắt bảng chọn cá nhân",
		"Not logged in":                                      "Chưa đăng nhập",
		"Please sign up or log in to edit the wiki.":         "Hãy mở tài khoản hoặc đăng nhập để sửa đổi wiki.", // this copy's own
		"Personal tools":                                     "Công cụ cá nhân",
		"Talk":                                               "Tin nhắn",
		"Discussion about edits from this IP address [alt-shift-n]": "Thảo luận với địa chỉ IP này [alt-shift-n]",
		"Contributions": "Đóng góp",
		"A list of edits made from this IP address [alt-shift-y]": "Danh sách các sửa đổi được thực hiện qua địa chỉ IP này [alt-shift-y]",
		"Create account": "Mở tài khoản",
		"You are encouraged to create an account and log in; however, it is not mandatory": "Bạn được khuyến khích mở tài khoản và đăng nhập; tuy nhiên, không bắt buộc phải có tài khoản",
		"Log in": "Đăng nhập",
		"You are encouraged to log in; however, it is not mandatory [alt-shift-o]": "Đăng nhập sẽ có lợi hơn, tuy nhiên không bắt buộc. [alt-shift-o]",
		"User menu":             "Bảng chọn thành viên",
		"Privacy policy":        "Quy định quyền riêng tư",
		"About Tolkien Gateway": "Giới thiệu Tolkien Gateway",
		"Source of this copy":   "Mã nguồn của bản sao này", // this copy's own
		"The largest encyclopedia dedicated to J.R.R. Tolkien and The Lord of the Rings.": "Bách khoa toàn thư lớn nhất về J.R.R. Tolkien và Chúa tể những chiếc nhẫn.", // the wiki's own footer text
		"Speak friend and edit": "Nói “bạn” rồi sửa đổi", // the wiki's own tagline

		// Page actions.
		"Share this page":                       "Chia sẻ trang này",
		"Link copied":                           "Đã sao chép URL vào bảng nhớ tạm!",
		"More languages":                        "Ngôn ngữ",
		"In other languages":                    "Ngôn ngữ khác",
		"Variants":                              "Biến thể",
		"Views":                                 "Giao diện",
		"Read":                                  "Đọc",
		"View source":                           "Xem mã nguồn",
		"You can view its source [alt-shift-e]": "Trang này được khóa. Bạn có thể xem mã nguồn. [alt-shift-e]",
		"View history":                          "Xem lịch sử",
		"Past revisions of this page [alt-shift-h]":                   "Các phiên bản cũ của trang này [alt-shift-h]",
		"View the content page [alt-shift-c]":                         "Xem trang nội dung [alt-shift-c]",
		"Discussion":                                                  "Thảo luận",
		"Discussion about the content page [alt-shift-t]":             "Thảo luận về trang này [alt-shift-t]",
		"What links here":                                             "Các liên kết đến đây",
		"A list of all wiki pages that link here [alt-shift-j]":       "Các trang liên kết đến đây [alt-shift-j]",
		"Related changes":                                             "Thay đổi liên quan",
		"Recent changes in pages linked from this page [alt-shift-k]": "Thay đổi gần đây của các trang liên kết đến đây [alt-shift-k]",
		"Printable version":                                           "Tải về bản in",
		"Printable version of this page [alt-shift-p]":                "Bản để in ra của trang [alt-shift-p]",
		"Permanent link":                                              "Liên kết thường trực",
		"Permanent link to this revision of this page":                "Liên kết thường trực đến phiên bản này của trang",
		"Page information":                                            "Thông tin trang",
		"More information about this page":                            "Thêm chi tiết về trang này",
		"Cite this page":                                              "Trích dẫn trang này",
		"Information on how to cite this page":                        "Hướng dẫn cách trích dẫn trang này",
		"Get shortened URL":                                           "Lấy URL ngắn gọn",
		"Download QR code":                                            "Tải mã QR",
		"More actions":                                                "Tùy chọn",
		"More":                                                        "Thêm",
		"More options":                                                "Thêm tùy chọn",
		"Tools":                                                       "Công cụ",
		"Page":                                                        "Trang",
		"Project page":                                                "Trang dự án",
		"File":                                                        "Tập tin",
		"Help page":                                                   "Trang trợ giúp",
		"Category":                                                    "Thể loại",
		"Special page":                                                "Trang đặc biệt",
		"Template":                                                    "Bản mẫu",

		// Search.
		"Advanced search":  "Tìm kiếm nâng cao",
		"Search media":     "Tìm tập tin",
		"No results found": "Không có kết quả",
		"Try the advanced search on Tolkien Gateway.": "Có thể thử các tùy chọn trên đây?",

		// Preferences.
		"Text":                          "Văn bản",
		"Color":                         "Màu",
		"Width":                         "Chiều rộng nội dung",
		"Pure black mode":               "Chế độ tối thuần túy",
		"Automatically hide navigation": "Tự động ẩn bộ điều hướng",
		"Small":                         "Nhỏ",
		"Standard":                      "Chuẩn",
		"Large":                         "Lớn",
		"Auto":                          "Tự động",
		"Light":                         "Sáng",
		"Dark":                          "Tối",
		"Wide":                          "Rộng",
		"Full":                          "Đầy đủ",
		"Off":                           "Tắt",
		"On":                            "Bật",

		// Page body furniture.
		"Contents":                         "Mục lục",
		"Back to top":                      "Quay lại đầu trang",
		"Toggle the table of contents":     "Bật tắt mục lục",
		"Last modified":                    "Sửa đổi lần cuối",
		"Copyright":                        "Bản quyền",
		"Source":                           "Nguồn",
		"Expand":                           "hiện",
		"Collapse":                         "ẩn",
		"expand":                           "mở",
		"collapse":                         "đóng",
		"Loading…":                         "đang tải…",
		"Retrieved from \"%s\"":            "Lấy từ “%s”",
		"This page was last edited on %s.": "Trang này được sửa đổi lần cuối vào %s.",
		"Content is available under %s unless otherwise noted.": "Nội dung được phát hành theo %s, ngoại trừ khi có ghi chú khác.",
		"Creative Commons Attribution-ShareAlike 4.0 License":   "Giấy phép Creative Commons Ghi công–Chia sẻ tương tự 4.0",
		"A static copy of %s on Tolkien Gateway, as of %s.":     "Bản sao tĩnh của %s trên Tolkien Gateway, theo %s.", // this copy's own
		"revision %d":                         "phiên bản %d", // this copy's own
		"From Tolkien Gateway":                "Từ Tolkien Gateway",
		"File on Tolkien Gateway":             "Tập tin trên Tolkien Gateway",
		"Category page":                       "Trang thể loại",
		"Information about Tolkien Gateway":   "Thông tin về Tolkien Gateway",
		"Categories":                          "Thể loại",
		"Hidden categories:":                  "Thể loại ẩn:",
		"Hidden category:":                    "Thể loại ẩn:",
		"Redirect to %s.":                     "Đổi hướng đến %s.",
		"There is no copy of this page here.": "Bản sao này không có trang này.", // this copy's own
		"Read %s on Tolkien Gateway.":         "Đọc %s trên Tolkien Gateway.",    // this copy's own
		"Finding a random page…":              "Đang tìm một trang ngẫu nhiên…",  // this copy's own
		"This page has not been translated into Vietnamese yet. It is shown in English.": "Trang này chưa được dịch sang tiếng Việt và đang hiển thị bằng tiếng Anh.", // this copy's own

		// File pages.
		"File history": "Lịch sử tập tin",
		"File usage":   "Trang sử dụng tập tin",
		"Click on a date/time to view the file as it appeared at that time.": "Nhấn vào ngày/giờ để xem nội dung tập tin tại thời điểm đó.",
		"Date/Time":                              "Ngày/Giờ",
		"Thumbnail":                              "Hình xem trước",
		"Dimensions":                             "Kích cỡ",
		"User":                                   "Thành viên",
		"Comment":                                "Miêu tả",
		"current":                                "hiện tại",
		"talk":                                   "thảo luận",
		"contribs":                               "đóng góp",
		"Original file":                          "Tập tin gốc",
		"Thumbnail for version as of %s":         "Hình xem trước của phiên bản lúc %s",
		"There are no pages that use this file.": "Không có trang nào sử dụng tập tin này.",
		"The following page uses this file:":     "Trang sau sử dụng tập tin này:",
		"The following %s pages use this file:":  "%s trang sau sử dụng tập tin này:",
		"%s × %s pixels":                         "%s×%s điểm ảnh",
		"Size of this preview: %s.":              "Kích thước hình xem trước: %s.",
		"Other resolutions: %s.":                 "Độ phân giải khác: %s.",
		"%s × %s pixels, file size: %s, MIME type: %s": "%s×%s điểm ảnh, kích thước tập tin: %s, kiểu MIME: %s",
		"file size: %s, MIME type: %s":                 "kích thước tập tin: %s, kiểu MIME: %s",

		// Category pages.
		"This category currently contains no pages or media.": "Thể loại này hiện không có trang hay tập tin nào.",
		"Subcategories": "Thể loại con",
		"This category has only the following subcategory.":                  "Thể loại này chỉ có thể loại con sau.",
		"This category has the following %s subcategories, out of %s total.": "Thể loại này có %s thể loại con sau, trên tổng số %s thể loại con.",
		"Pages in category \"%s\"":                                           "Trang trong thể loại “%s”",
		"This category contains only the following page.":                    "Thể loại này chỉ chứa trang sau.",
		"The following %s pages are in this category, out of %s total.":      "Thể loại này chứa %s trang sau, trên tổng số %s trang.",
		"Media in category \"%s\"":                                           "Tập tin trong thể loại “%s”",
		"This category contains only the following file.":                    "Thể loại này gồm tập tin sau.",
		"The following %s files are in this category, out of %s total.":      "%s tập tin sau nằm trong thể loại này, trong tổng số %s tập tin.",
		"Contains %s subcategories, %s pages, and %s files":                  "Có %s thể loại con, %s trang, và %s tập tin",
		"empty": "trống",
	},
}

// ui is the interface text in the site's language, falling back to English.
func (s *site) ui(en string) string {
	if t, ok := uiMessages[s.lang][en]; ok {
		return t
	}
	return en
}

// uif formats an interface message.
func (s *site) uif(en string, args ...any) string {
	return fmt.Sprintf(s.ui(en), args...)
}

// num groups digits as the wiki's interface does in the site's language.
func (s *site) num(n int64) string {
	if s.lang == "vi" {
		return strings.ReplaceAll(formatNum(n), ",", ".")
	}
	return formatNum(n)
}

// size is Language::formatSize in the site's language.
func (s *site) size(n int64) string {
	out := formatSize(n)
	if s.lang != "vi" {
		return out
	}
	// Vietnamese writes the decimal comma, the grouping dot and "kB".
	value, unit, _ := strings.Cut(out, " ")
	value = strings.NewReplacer(",", ".", ".", ",").Replace(value)
	switch unit {
	case "bytes":
		unit = "byte"
	case "KB":
		unit = "kB"
	}
	return value + " " + unit
}

// dateTime formats a time as the wiki's footer does in the site's
// language: "1 August 2026, at 20:11", "ngày 1 tháng 8 năm 2026, 20:11".
func (s *site) dateTime(t time.Time) string {
	t = t.UTC()
	if s.lang == "vi" {
		return fmt.Sprintf("ngày %d tháng %d năm %d, %s", t.Day(), int(t.Month()), t.Year(), t.Format("15:04"))
	}
	return lastEdited(t)
}

// historyTime is the file history's "20:53, 7 August 2022".
func (s *site) historyTime(t time.Time) string {
	t = t.UTC()
	if s.lang == "vi" {
		return fmt.Sprintf("%s, ngày %d tháng %d năm %d", t.Format("15:04"), t.Day(), int(t.Month()), t.Year())
	}
	return historyTime(t)
}

// relativeTime is the sidebar's "3 months ago" until the script recomputes
// it in the reader's own clock.
func (s *site) relativeTime(t, now time.Time) string {
	en := relativeTime(t, now)
	if s.lang != "vi" {
		return en
	}
	switch en {
	case "now":
		return "bây giờ"
	case "yesterday":
		return "hôm qua"
	}
	n, unit, _ := strings.Cut(strings.TrimSuffix(en, " ago"), " ")
	vi := map[string]string{"year": "năm", "month": "tháng", "week": "tuần", "day": "ngày", "hour": "giờ", "minute": "phút"}[strings.TrimSuffix(unit, "s")]
	if _, err := strconv.Atoi(n); err != nil || vi == "" {
		return en
	}
	return n + " " + vi + " trước"
}
