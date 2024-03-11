@ammario collected these results on 2024-03-11 by running `labeler test` on
`coder/coder`.

Note:
* the performance is quite bad partially because of an inconsistent
use of labels in coder/coder
* the context window of gpt-3.5-turbo is 16k, which is partially the cause
of inferior performance

### gpt-3.5-turbo

```
Total issues:      98
False adds:        92 93.88%
Top false adds:    [{site 10} {chore 10} {feature 10} {bug 8} {customer-requested 6}]
False removes:     74 75.51%
Top false removes: [{site 18} {feature 7} {s3 6} {customer-requested 5} {s4 4}]
Hits:              100 102.04%
Top hit labels:    [{bug 29} {feature 21} {chore 14} {site 11} {flake 7}]
Tokens used:       1164078
```

## gpt-4-turbo-preview

```
Total issues:      100
False adds:        53 53.00%
Top false adds:    [{bug 7} {site 7} {s3 6} {docs 4} {feature 4}]
False removes:     68 68.00%
Top false removes: [{chore 8} {bug 8} {s3 5} {feature 5} {api 4}]
Hits:              106 106.00%
Top hit labels:    [{site 25} {bug 24} {feature 23} {chore 8} {flake 7}]
Tokens used:       2296845
```