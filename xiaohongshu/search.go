package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/errors"
)

type SearchResult struct {
	Search struct {
		Feeds FeedsValue `json:"feeds"`
	} `json:"search"`
}

// FilterOption 筛选选项结构体
type FilterOption struct {
	SortBy      string `json:"sort_by,omitempty" jsonschema:"排序依据: 综合|最新|最多点赞|最多评论|最多收藏,默认为'综合'"`
	NoteType    string `json:"note_type,omitempty" jsonschema:"笔记类型: 不限|视频|图文,默认为'不限'"`
	PublishTime string `json:"publish_time,omitempty" jsonschema:"发布时间: 不限|一天内|一周内|半年内,默认为'不限'"`
	SearchScope string `json:"search_scope,omitempty" jsonschema:"搜索范围: 不限|已看过|未看过|已关注,默认为'不限'"`
	Location    string `json:"location,omitempty" jsonschema:"位置距离: 不限|同城|附近,默认为'不限'"`
}

// internalFilterOption 内部使用的筛选选项(基于索引)
type internalFilterOption struct {
	FiltersIndex int    // 筛选组索引
	TagsIndex    int    // 标签索引
	Text         string // 标签文本描述
}

// 预定义的筛选选项映射表（内部使用）
var filterOptionsMap = map[int][]internalFilterOption{
	1: { // 排序依据
		{FiltersIndex: 1, TagsIndex: 1, Text: "综合"},
		{FiltersIndex: 1, TagsIndex: 2, Text: "最新"},
		{FiltersIndex: 1, TagsIndex: 3, Text: "最多点赞"},
		{FiltersIndex: 1, TagsIndex: 4, Text: "最多评论"},
		{FiltersIndex: 1, TagsIndex: 5, Text: "最多收藏"},
	},
	2: { // 笔记类型 (DOM有隐藏重复: 不限,视频,视频hidden,图文hidden,图文visible)
		{FiltersIndex: 2, TagsIndex: 1, Text: "不限"},
		{FiltersIndex: 2, TagsIndex: 2, Text: "视频"},
		{FiltersIndex: 2, TagsIndex: 5, Text: "图文"},
	},
	3: { // 发布时间
		{FiltersIndex: 3, TagsIndex: 1, Text: "不限"},
		{FiltersIndex: 3, TagsIndex: 2, Text: "一天内"},
		{FiltersIndex: 3, TagsIndex: 3, Text: "一周内"},
		{FiltersIndex: 3, TagsIndex: 4, Text: "半年内"},
	},
	4: { // 搜索范围
		{FiltersIndex: 4, TagsIndex: 1, Text: "不限"},
		{FiltersIndex: 4, TagsIndex: 2, Text: "已看过"},
		{FiltersIndex: 4, TagsIndex: 3, Text: "未看过"},
		{FiltersIndex: 4, TagsIndex: 4, Text: "已关注"},
	},
	5: { // 位置距离
		{FiltersIndex: 5, TagsIndex: 1, Text: "不限"},
		{FiltersIndex: 5, TagsIndex: 2, Text: "同城"},
		{FiltersIndex: 5, TagsIndex: 3, Text: "附近"},
	},
}

// convertToInternalFilters 将 FilterOption 转换为内部的 internalFilterOption 列表
func convertToInternalFilters(filter FilterOption) ([]internalFilterOption, error) {
	var internalFilters []internalFilterOption

	// 处理排序依据
	if filter.SortBy != "" {
		internal, err := findInternalOption(1, filter.SortBy)
		if err != nil {
			return nil, fmt.Errorf("排序依据错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	// 处理笔记类型
	if filter.NoteType != "" {
		internal, err := findInternalOption(2, filter.NoteType)
		if err != nil {
			return nil, fmt.Errorf("笔记类型错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	// 处理发布时间
	if filter.PublishTime != "" {
		internal, err := findInternalOption(3, filter.PublishTime)
		if err != nil {
			return nil, fmt.Errorf("发布时间错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	// 处理搜索范围
	if filter.SearchScope != "" {
		internal, err := findInternalOption(4, filter.SearchScope)
		if err != nil {
			return nil, fmt.Errorf("搜索范围错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	// 处理位置距离
	if filter.Location != "" {
		internal, err := findInternalOption(5, filter.Location)
		if err != nil {
			return nil, fmt.Errorf("位置距离错误: %w", err)
		}
		internalFilters = append(internalFilters, internal)
	}

	return internalFilters, nil
}

// findInternalOption 根据筛选组索引和文本查找内部筛选选项
func findInternalOption(filtersIndex int, text string) (internalFilterOption, error) {
	options, exists := filterOptionsMap[filtersIndex]
	if !exists {
		return internalFilterOption{}, fmt.Errorf("筛选组 %d 不存在", filtersIndex)
	}

	for _, option := range options {
		if option.Text == text {
			return option, nil
		}
	}

	return internalFilterOption{}, fmt.Errorf("在筛选组 %d 中未找到文本 '%s'", filtersIndex, text)
}

// validateInternalFilterOption 验证内部筛选选项是否在有效范围内
func validateInternalFilterOption(filter internalFilterOption) error {
	// 检查筛选组索引是否有效
	if filter.FiltersIndex < 1 || filter.FiltersIndex > 5 {
		return fmt.Errorf("无效的筛选组索引 %d，有效范围为 1-5", filter.FiltersIndex)
	}

	// 检查筛选组是否存在
	options, exists := filterOptionsMap[filter.FiltersIndex]
	if !exists {
		return fmt.Errorf("筛选组 %d 不存在", filter.FiltersIndex)
	}

	// 检查TagsIndex是否在已定义的选项中（而不是简单的数组长度检查）
	found := false
	for _, opt := range options {
		if opt.TagsIndex == filter.TagsIndex {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("筛选组 %d 中未定义 TagsIndex=%d", filter.FiltersIndex, filter.TagsIndex)
	}

	return nil
}

type SearchAction struct {
	page *rod.Page
}

func NewSearchAction(page *rod.Page) *SearchAction {
	pp := page.Timeout(60 * time.Second)

	return &SearchAction{page: pp}
}

func (s *SearchAction) Search(ctx context.Context, keyword string, filters ...FilterOption) ([]Feed, error) {
	page := s.page.Context(ctx)

	searchURL := makeSearchURL(keyword)
	page.MustNavigate(searchURL)
	page.MustWaitStable()

	// 等待搜索结果加载（使用DOM元素而非__INITIAL_STATE__）
	page.MustWait(`() => document.querySelectorAll('section.note-item').length > 0`)

	// 如果有筛选条件，则应用筛选
	if len(filters) > 0 {
		// 将所有 FilterOption 转换为内部筛选选项
		var allInternalFilters []internalFilterOption
		for _, filter := range filters {
			internalFilters, err := convertToInternalFilters(filter)
			if err != nil {
				return nil, fmt.Errorf("筛选选项转换失败: %w", err)
			}
			allInternalFilters = append(allInternalFilters, internalFilters...)
		}

		// 验证所有内部筛选选项
		for _, filter := range allInternalFilters {
			if err := validateInternalFilterOption(filter); err != nil {
				return nil, fmt.Errorf("筛选选项验证失败: %w", err)
			}
		}

		// 悬停在筛选按钮上
		filterButton := page.MustElement(`div.filter`)
		filterButton.MustHover()

		// 等待筛选面板出现
		page.MustWait(`() => document.querySelector('div.filter-panel') !== null`)

		// 应用筛选条件 - 用 CSS 选择器点击
		for _, filter := range allInternalFilters {
			logrus.Infof("Clicking filter: group=%d, tag=%d, text='%s'", filter.FiltersIndex, filter.TagsIndex, filter.Text)
			// 选择器：div.filters:nth-child(N) 内的 .tag-container 内的第 M 个 .tags
			selector := fmt.Sprintf(`div.filter-panel div.filters:nth-child(%d) .tag-container .tags:nth-child(%d)`,
				filter.FiltersIndex, filter.TagsIndex)
			logrus.Infof("Using selector: %s", selector)
			option, err := page.Element(selector)
			if err != nil {
				logrus.Warnf("Failed to find element with selector '%s': %v", selector, err)
				continue
			}
			if option == nil {
				logrus.Warnf("Element not found for selector '%s'", selector)
				continue
			}
			option.MustClick()
			logrus.Infof("Successfully clicked '%s'", filter.Text)
			page.MustWaitStable()
		}

		// 等待页面更新
		page.MustWaitStable()
		// 等待搜索结果重新加载
		page.MustWait(`() => document.querySelectorAll('section.note-item').length > 0`)
	}

	// 从DOM提取搜索结果（rednote.com不使用__INITIAL_STATE__）
	return s.extractFeedsFromDOM(page)
}

// extractFeedsFromDOM 从DOM元素中提取Feed数据
func (s *SearchAction) extractFeedsFromDOM(page *rod.Page) ([]Feed, error) {
	result := page.MustEval(`() => {
		const items = document.querySelectorAll('section.note-item');
		const feeds = [];

		items.forEach(item => {
			try {
				// 提取标题 - 尝试多种selector
				let title = '';
				const titleSelectors = [
					'.footer .title span',
					'.footer .title',
					'.note-content .title',
					'a.title span',
					'.title span',
					'.desc'
				];
				for (const sel of titleSelectors) {
					const el = item.querySelector(sel);
					if (el && el.textContent.trim()) {
						title = el.textContent.trim();
						break;
					}
				}

				// 提取笔记ID和xsec_token（从链接中）
				const linkEl = item.querySelector('a[href*="/search_result/"]') || item.querySelector('a[href*="/explore/"]');
				let noteId = '';
				let xsecToken = '';
				if (linkEl) {
					const href = linkEl.getAttribute('href');
					// 匹配 /search_result/{id} 或 /explore/{id}
					const match = href.match(/\/search_result\/([a-f0-9]+)/) || href.match(/\/explore\/([a-f0-9]+)/);
					if (match) {
						noteId = match[1];
					}
					// 提取xsec_token（可能包含特殊字符如=）
					const tokenMatch = href.match(/xsec_token=([^&]*)/);
					if (tokenMatch) {
						xsecToken = tokenMatch[1]; // 不decode，保持原样
					}
				}

				// 提取封面图片 - 尝试多种selector
				let coverUrl = '';
				const coverSelectors = ['a.cover img', '.cover img', 'img.cover', 'a img'];
				for (const sel of coverSelectors) {
					const el = item.querySelector(sel);
					if (el) {
						coverUrl = el.getAttribute('src') || el.getAttribute('data-src') || '';
						if (coverUrl) break;
					}
				}

				// 提取点赞数 - 尝试多种selector
				let likeCount = '0';
				const likeSelectors = ['.like-wrapper .count', '.like-wrapper span:last-child', '.like span', '.engage-bar .like', '[class*="like"] span'];
				for (const sel of likeSelectors) {
					const el = item.querySelector(sel);
					if (el && el.textContent.trim()) {
						likeCount = el.textContent.trim();
						break;
					}
				}

				// 提取作者信息 - 尝试多种selector
				let authorName = '';
				const authorSelectors = ['.author-wrapper .name', '.author .name', '.author-wrapper .author-name', '.nickname', '.user-name'];
				for (const sel of authorSelectors) {
					const el = item.querySelector(sel);
					if (el && el.textContent.trim()) {
						authorName = el.textContent.trim();
						break;
					}
				}

				// 提取头像
				let avatarUrl = '';
				const avatarSelectors = ['.author-wrapper .author-avatar img', '.author img', '.avatar img'];
				for (const sel of avatarSelectors) {
					const el = item.querySelector(sel);
					if (el) {
						avatarUrl = el.getAttribute('src') || '';
						if (avatarUrl) break;
					}
				}

				if (noteId) {
					feeds.push({
						id: noteId,
						xsecToken: xsecToken,
						noteCard: {
							displayTitle: title,
							cover: {
								url: coverUrl
							},
							interactInfo: {
								likedCount: likeCount
							},
							user: {
								nickname: authorName,
								avatar: avatarUrl
							}
						}
					});
				}
			} catch (e) {
				console.error('Error extracting item:', e);
			}
		});

		return JSON.stringify(feeds);
	}`).String()

	if result == "" || result == "[]" {
		return nil, errors.ErrNoFeeds
	}

	var feeds []Feed
	if err := json.Unmarshal([]byte(result), &feeds); err != nil {
		return nil, fmt.Errorf("failed to unmarshal feeds: %w", err)
	}

	return feeds, nil
}

func makeSearchURL(keyword string) string {

	values := url.Values{}
	values.Set("keyword", keyword)
	values.Set("source", "web_explore_feed")

	//https://www.rednote.com/search_result?keyword=%25E7%258E%258B%25E5%25AD%2590&source=web_search_result_notes
	//https://www.rednote.com/search_result?keyword=%25E7%258E%258B%25E5%25AD%2590&source=web_explore_feed
	return fmt.Sprintf("https://www.rednote.com/search_result?%s", values.Encode())
}
