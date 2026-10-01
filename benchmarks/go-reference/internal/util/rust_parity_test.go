package util

import (
	"encoding/json"
	"os"
	"testing"
)

func TestExportRustParity(t *testing.T) {
	if os.Getenv("RUST_PARITY_OUTPUT") == "" {
		t.Skip("offline golden export requires RUST_PARITY_OUTPUT")
	}
	var inputs []string
	if e := json.Unmarshal([]byte(`["http://", "http://amazon.ca/some-product?utm_source=foo", "http://example.com", "http://forums.redflagdeals.com/deal-123/", "https://", "https://amazon.ca.store.example/dp/example?product=coffee", "https://amazon.ca/dp/123", "https://amazon.ca/dp/12345", "https://amazon.ca/dp/12345?tag=example-20", "https://amazon.ca/dp/12345?tag=old-tag", "https://amazon.ca/dp/123?tag=someone-20", "https://amazon.ca/dp/B07YF3JQF8?psc=1&smid=A1M4A2O2C9P4N4", "https://amazon.ca/gp/product/B07YF3JQF8/ref=ox_sc_act_title_1?smid=A1M4A2O2C9P4N4&psc=1", "https://bestbuy.ca/en-ca/product/12345", "https://bestbuy.ca/product", "https://bestbuyca.o93x.net/c/111/222/333?u=", "https://bestbuyca.o93x.net/c/111/222/333?u=https%3A%2F%2Fbestbuy.ca%2Fen-ca%2Fproduct%2F12345", "https://bestbuyca.o93x.net/c/111/222/333?u=https%3A%2F%2Fbestbuy.ca%2Fproduct", "https://bestbuyca.o93x.net/c/123/456/789?subId1=foo&u=https://bestbuy.ca/product", "https://bestbuyca.o93x.net/c/123/456/789?u=https%3A%2F%2Fbestbuy.ca%2Fproduct", "https://bestbuyca.o93x.net/c/123/456/789?u=https://bestbuy.ca/product", "https://click.linksynergy.com/?murl=", "https://click.linksynergy.com/link?murl=https%3A%2F%2Fexample.com%2Fproduct", "https://click.linksynergy.com/link?murl=javascript%3Aalert(1)", "https://example.com", "https://example.com/deal", "https://example.com/product", "https://forums.redflagdeals.com/deal", "https://forums.redflagdeals.com/deal-123", "https://forums.redflagdeals.com/deal?rfd_sk=tt&sd=d", "https://forums.redflagdeals.com/deal?utm_source=foo&utm_medium=bar", "https://forums.redflagdeals.com/my-deal", "https://forums.redflagdeals.com/my-deal-1234567", "https://forums.redflagdeals.com/my-deal-1234567/", "https://go.redirectingat.com/?url=", "https://go.redirectingat.com/?url=ftp%3A%2F%2Fexample.com%2Ffile", "https://go.redirectingat.com/?url=https%3A%2F%2Fexample.com%2Fdeal", "https://go.redirectingat.com/?url=https%3A%2F%2Fwww.ebay.ca%2Fitm%2F134954474751%3F_trkparms%3Dispr%253D1", "https://notbestbuy.ca/search?product=coffee", "https://store.example/amazon.ca/search?product=coffee", "https://store.example/products/A%2FB?query=C%2B%2B&filter=a%26b", "https://www.amazon.ca/dp/B07YF3JQF8", "https://www.amazon.ca/dp/B07YF3JQF8/ref=pd_bxgy_d_sccl_1/130-0878020-0818067?pd_rd_w=Xo1Zs&content-id=amzn1.sym", "https://www.amazon.ca/s?filter=a%26b&k=C%2B%2B", "https://www.amazon.ca/s?k=coffee&tag=tracking&utm_source=fixture", "https://www.amazon.com/Apple-AirPods-Pro-2nd-Gen/dp/B0D1XD1ZV3/ref=sr_1_1?crid=123", "https://www.amazon.com/dp/B08P2H15Y?psc=1&th=1", "https://www.amazon.com/dp/B08P2H15Y?th=1&psc=1&ref_=nav_em", "https://www.amazon.com/dp/B0D1XD1ZV3", "https://www.bestbuy.ca/en-ca/product/apple-airpods-pro-2nd-generation-with-magsafe-charging-case-usb-c/17395420", "https://www.bestbuy.ca/en-ca/product/apple-airpods-pro-2nd-generation-with-magsafe-charging-case-usb-c/17395420?cmp=seo-17395420&irclickid=abc", "https://www.bestbuy.ca/en-ca/search?search=coffee&cmp=tracking&utm_source=fixture", "https://www.bestbuy.com/site/apple-airpods-pro-2nd-generation/6536962.p", "https://www.bestbuy.com/site/apple-airpods-pro-2nd-generation/6536962.p?loc=137454&cmp=RMX&ref=199", "https://www.bestbuy.com/site/searchpage.jsp?st=coffee&cmp=tracking", "https://www.ebay.ca/itm/123456789012", "https://www.ebay.ca/itm/123456789012?campid=example", "https://www.ebay.ca/itm/134954474751", "https://www.ebay.ca/itm/134954474751?_skw=laptop&_trkparms=ispr%3D1&hash=item1f6bf870ff:g:abc", "https://www.ebay.ca/sch/i.html?_nkw=coffee&_trksid=tracking&utm_source=fixture", "https://www.ebay.com/itm/123456789012", "https://www.ebay.com/itm/134954474751", "https://www.ebay.com/itm/Apple-MacBook-Pro-16-inch-M3-Pro/123456789012?amdata=enc%3A123", "https://www.ebay.com/p/12345", "https://www.ebay.com/p/12345?iid=134954474751&thm=1000", "https://www.ebay.com/p/12345?thm=1000", "https://www.forums.redflagdeals.com/my-deal/", "https://www.homedepot.ca/product/dewalt-20v-max/10001234?custom=123"]`), &inputs); e != nil {
		t.Fatal(e)
	}
	out := []map[string]any{}
	for _, s := range inputs {
		for _, tag := range []string{"", "fixture-20"} {
			for _, prefix := range []string{"", "https://bestbuyca.o93x.net/c/fixture?u="} {
				r, _ := CleanReferralLink(s, tag, prefix)
				out = append(out, map[string]any{"url": s, "product": CleanProductURL(s), "referral": r, "tag": tag, "prefix": prefix})
			}
		}
	}
	raw, e := json.Marshal(out)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(os.Getenv("RUST_PARITY_OUTPUT"), raw, 0600); e != nil {
		t.Fatal(e)
	}
}
