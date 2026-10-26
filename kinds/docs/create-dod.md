# Create the DOD

The DOD is a Jenkins pipeline that creates a dev namespace with your services.

1. Open the DOD pipeline in Jenkins.
2. Choose **Build with Parameters** and fill in:
   - **Namespace:** `${namespace}`
   - **Services:** `${services}`
3. Start the build and wait for it to finish successfully.

When the build is green, confirm to continue.