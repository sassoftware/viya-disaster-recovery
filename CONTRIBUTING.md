# How to Contribute
This project is community-driven, and we'd love to accept your patches and contributions.
We just ask that you follow our contribution guidelines when you do. Refer
to the [Contributor Handbook](https://sassoftware.github.io/contributor-handbook.html)
for guidance.

## Contributor License Agreement
Contributions to this project must be accompanied by a signed [Contributor Agreement](https://github.com/sassoftware/viya-disaster-recovery/blob/main/ContributorAgreement.txt).
You (or your employer) retain the copyright to your contribution; this agreement simply grants
us permission to use and redistribute your contributions as part of the project.

## Code Reviews
All submissions to this project—including submissions from project members—require
review. Our review process typically involves performing unit tests, development
tests, integration tests, and security scans.

## How To Open A Pull Request

The following steps below demonstrate how to contribute to the [viya-disaster-recovery](https://github.com/sassoftware/viya-disaster-recovery/)
repository by forking it, making changes, and submitting a pull request (PR).

1. Fork the Repository

    - Navigate to the [viya-disaster-recovery](https://github.com/sassoftware/viya-disaster-recovery/).
    - Click the **"Fork"** button in the upper-right corner.
    - This creates a copy of the repository under your GitHub account.

    **Alternative (using GitHub CLI):**
    If you have the [GitHub CLI](https://cli.github.com/) installed, run:

    ```bash
    gh repo fork https://github.com/sassoftware/viya-disaster-recovery.git --clone
    ```

2. Clone the Forked Repository Locally

    ```bash
    git clone https://github.com/<YOUR_USERNAME>/<REPO_NAME>.git
    ```

3. Add the Original Repository as an Upstream Remote (Optional but recommended)

    - To keep your fork in sync with the original [viya-disaster-recovery](https://github.com/sassoftware/viya-disaster-recovery/)
    repository:

    ```bash
    git remote add upstream https://github.com/sassoftware/viya-disaster-recovery.git
    git fetch upstream
    ```

    - To sync changes from the original repo:

    ```bash
    git checkout main
    git pull upstream main
    git push origin main
    ```

4. Create a New Branch for Your Contribution

    ```bash
    git checkout -b my-contribution-branch
    ```

5. Make Your Changes Locally

    - Edit the files as needed using your preferred code editor.

6. Stage and Commit Your Changes

    ```bash
    git add .
    git commit -s -m "Your conventional commit message"
    ```

7. Push the Branch to Your Fork

    ```bash
    git push origin my-contribution-branch
    ```

8. Create the Pull Request (PR)

    - Go to your forked repository on GitHub.
    - You will see a **"Compare & pull request"** button, click it.
    - Check to ensure:
        - The **base repository** is the original [viya-disaster-recovery](https://github.com/sassoftware/viya-disaster-recovery/)
        repository.
        - The **base branch** is `main`.
        - The **head repository** and **compare branch** is correct.
    - Click **"Create pull request."**

9. Keep Your Branch Up to Date (If Needed)

    - If the base branch has changed and you need to rebase:

    ```bash
    git fetch upstream
    git checkout my-contribution-branch
    git rebase upstream/main
    ```

    - After resolving any conflicts, force push your changes:

    ```bash
    git push origin my-contribution-branch --force
    ```

## Pull Request Requirements

### Conventional Commits
All pull requests must follow the [Conventional Commit](https://www.conventionalcommits.org/en/v1.0.0/)
standard for commit messages. This helps maintain a consistent and meaningful
commit history. Pull requests with commits that do not follow the Conventional
Commit format will not be merged.

### Developer Certificate of Origin Sign-Off
This project requires all commits to be signed off in accordance with the [Developer Certificate of Origin (DCO)](https://developercertificate.org/).
By signing off your commits, you certify that you have the right to submit the
contribution under the open source license used by this project.

To sign off your commits, use the --signoff flag with git commit:

```bash
git commit --signoff -m "Your commit message"
```

This will add a Signed-off-by line to your commit message, e.g.:

```bash
Signed-off-by: Your Name <your.email@example.com>
```

For more information, please refer to https://probot.github.io/apps/dco/


## Security Scans
To ensure that all submissions meet our security and quality standards, we perform
security scans using internal SAS infrastructure. Contributions might be subjected
to security scans before they can be accepted. Reporting of any Common Vulnerabilities
and Exposures (CVEs) that are detected is not available in this project at this
time.
