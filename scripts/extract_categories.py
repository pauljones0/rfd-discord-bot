import pathlib
import re

script_dir = pathlib.Path(__file__).parent.resolve()
html_file = script_dir.parent / "testdata" / "hot-deals.html"
out_file = script_dir.parent / "docs" / "categories.txt"

with open(html_file, 'r', encoding='utf-8') as f:
    content = f.read()

matches = re.findall(r'class="category_item[^"]*"[^>]*data-name="?([^">]+)"?', content)
unique_cats = sorted(list(set(matches)))

with open(out_file, 'w', encoding='utf-8') as f:
    for cat in unique_cats:
        f.write(cat + '\n')

